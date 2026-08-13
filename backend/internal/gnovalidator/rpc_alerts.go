package gnovalidator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"gorm.io/gorm"
)

// rpcAlertAddr is the alert_logs addr used for chain-wide RPC alerts. It
// mirrors the "all" convention used by the blockchain-stuck alert, but stays
// distinct so an RPC outage is never mistaken for a validator incident or for
// a chain halt. Every validator-scoped query in db_score.go excludes it.
const rpcAlertAddr = "rpc"

// rpcAlertQueueSize bounds how many pending dispatch jobs the single
// per-chain worker (see NewRPCObserver) will buffer. Kept small deliberately:
// a healthy chain never has more than one or two RPC alerts in flight
// (CRITICAL then RESOLVED), so a queue this size only ever fills up when
// something is genuinely stuck (e.g. every webhook target is unreachable),
// at which point buffering more is not useful — see the drop policy in
// NewRPCObserver.
const rpcAlertQueueSize = 8

// rpcAlertEntry is the per-chain cooldown/pairing bookkeeping for RPC outage
// alerts.
//
//   - lastDispatch is the time of the last CRITICAL that was actually handed
//     to the dispatch queue (not merely decided upon — see unannounceRPCAlert).
//     It is the anchor the cooldown is measured from, and — this is the fix
//     for the flapping-endpoint bug — it is deliberately NOT reset when the
//     outage resolves. Resetting it on every RESOLVED is what let a flapping
//     endpoint (down/up/down/up every health-check tick) send one CRITICAL
//     and one RESOLVED per flap forever: the pool always pairs an
//     EventAllDown edge with exactly one later EventRecovered edge (see
//     rpcpool.Client.onAllDown/onSuccess), so if recovery unconditionally
//     re-armed the gate, every single flap would look like "the first outage
//     ever" to shouldSendRPCAlert.
//   - announced is true from the moment a CRITICAL is committed to the
//     dispatch queue until its paired RESOLVED is (or, on a dropped enqueue,
//     until the commitment is rolled back). It is what lets shouldSendRPCResolved
//     tell a genuine recovery from a stray EventRecovered with nothing to pair
//     against.
type rpcAlertEntry struct {
	lastDispatch time.Time
	announced    bool
}

var (
	rpcAlertMu     sync.Mutex
	rpcAlertStates = make(map[string]*rpcAlertEntry)
)

func rpcAlertEntryFor(chainID string) *rpcAlertEntry {
	e, ok := rpcAlertStates[chainID]
	if !ok {
		e = &rpcAlertEntry{}
		rpcAlertStates[chainID] = e
	}
	return e
}

// shouldSendRPCAlert reports whether a CRITICAL for chainID's current outage
// may be dispatched at now, given cooldown measured against the last CRITICAL
// that was actually committed to dispatch (see rpcAlertEntry.lastDispatch —
// a RESOLVED never resets this). When it returns true it commits: it records
// the dispatch time and marks the outage "announced" so the paired RESOLVED
// is not later treated as an orphan. Callers that decide, after a true
// result, that the event will not actually reach the wire (e.g. the dispatch
// queue was full) must call unannounceRPCAlert to keep the bookkeeping
// honest.
func shouldSendRPCAlert(chainID string, now time.Time, cooldown time.Duration) bool {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	e := rpcAlertEntryFor(chainID)
	if !e.lastDispatch.IsZero() && now.Sub(e.lastDispatch) < cooldown {
		return false
	}
	e.lastDispatch = now
	e.announced = true
	return true
}

// shouldSendRPCResolved reports whether a RESOLVED for chainID may be
// dispatched: only when a CRITICAL for the current outage was actually
// announced (i.e. shouldSendRPCAlert returned true and was not rolled back).
// This is the other half of the flapping-endpoint fix: a CRITICAL suppressed
// by cooldown must never be followed by a RESOLVED — that would be an orphan
// "all clear" for an outage nobody was ever told about. It always clears
// announced on a true result so the next outage starts from a clean pairing
// state, without touching lastDispatch (the cooldown clock survives).
func shouldSendRPCResolved(chainID string) bool {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	e, ok := rpcAlertStates[chainID]
	if !ok || !e.announced {
		return false
	}
	e.announced = false
	return true
}

// unannounceRPCAlert clears chainID's "announced" flag after a
// shouldSendRPCAlert commitment could not actually be handed to the dispatch
// queue (queue full). This only undoes the pairing half of the commitment:
// without it, a dropped CRITICAL would still count as "announced", and its
// paired RESOLVED would later fire for an alert nobody was ever sent.
//
// It deliberately leaves lastDispatch untouched, unlike an earlier version of
// this function that deleted the whole entry. A full dispatch queue is most
// likely to occur under sustained backpressure (many webhooks/Telegram chats,
// each dispatch bounded at ~10s) — exactly the condition under which a
// flapping endpoint is also most likely. Wiping lastDispatch there would
// make the very next EventAllDown look like "the first outage ever" and
// bypass whatever remained of the cooldown, letting the cooldown collapse to
// the health-check interval right after a congestion episode — reintroducing
// a narrower version of the spam this whole cooldown exists to prevent. So
// the cooldown clock survives a dropped dispatch exactly as it survives a
// genuine RESOLVED (see shouldSendRPCResolved).
func unannounceRPCAlert(chainID string) {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	if e, ok := rpcAlertStates[chainID]; ok {
		e.announced = false
	}
}

// resetRPCAlertState clears all per-chain state. Test helper.
func resetRPCAlertState() {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	rpcAlertStates = make(map[string]*rpcAlertEntry)
}

// rpcJob is one dispatch task handed from the (fast, synchronous) observer to
// the (slow, blocking) per-chain worker goroutine.
type rpcJob struct {
	db      *gorm.DB
	chainID string
	level   string // "CRITICAL" or "RESOLVED"
	msg     string
	data    internal.AlertData
}

// rpcDispatchFunc performs the actual outage/recovery notification: the
// webhook/Telegram fan-out plus the alert_logs row. Swappable via
// setRPCDispatch so tests can observe dispatch calls without doing real
// network or DB I/O; guarded by rpcDispatchMu so swapping it in a test and
// reading it from a live worker goroutine is race-free regardless of timing.
var (
	rpcDispatchMu sync.RWMutex
	rpcDispatch   = defaultRPCDispatch
)

func getRPCDispatch() func(rpcJob) {
	rpcDispatchMu.RLock()
	defer rpcDispatchMu.RUnlock()
	return rpcDispatch
}

// setRPCDispatch overrides the dispatch function. Test seam only; production
// code always uses defaultRPCDispatch.
func setRPCDispatch(fn func(rpcJob)) {
	rpcDispatchMu.Lock()
	defer rpcDispatchMu.Unlock()
	rpcDispatch = fn
}

func defaultRPCDispatch(job rpcJob) {
	log.Println(job.msg)
	if sendErr := internal.SendInfoValidator(job.chainID, job.data, job.db); sendErr != nil {
		log.Printf("[rpc][%s] SendInfoValidator error: %v", job.chainID, sendErr)
	}
	// skipped=false (not true) is load-bearing: SendResolveAlerts only
	// treats alert_logs rows with skipped=true as pending validator
	// incidents to auto-resolve. Passing false here is what keeps it from
	// ever picking up this RPC-outage CRITICAL as one.
	if logErr := database.InsertAlertlog(job.db, job.chainID, rpcAlertAddr, rpcAlertAddr, job.level, 0, 0, false, time.Now(), job.msg); logErr != nil {
		log.Printf("[rpc][%s] InsertAlertlog error: %v", job.chainID, logErr)
	}
}

// runRPCAlertWorker drains queue and dispatches each job in order, one at a
// time, until ctx is done. A single worker (rather than a goroutine per
// event) is deliberate: it is what guarantees a RESOLVED can never be
// delivered before its paired CRITICAL, since both share the same chain-scoped
// queue and are handled strictly FIFO.
//
// A bare select{} between ctx.Done() and queue gives no priority between its
// cases: with both ready (ctx cancelled — e.g. an admin-triggered chain
// restart — while a job still sits in queue), Go picks pseudo-randomly
// between them, so the worker could return without ever dispatching or even
// acknowledging that job. Rather than let that be a silent drop — the one
// path in this file that would otherwise go unlogged, unlike the full-queue
// path in NewRPCObserver — the shutdown check runs first, non-blocking, on
// every iteration: once ctx is done, the next iteration always takes the
// drain-and-return path deterministically, never the queue-read path, no
// matter how much is still buffered.
//
// Whatever is drained here is not delivered: the chain's monitoring context
// just ended, so dispatching now would race whatever the next pool for this
// chain does, and a stale "still down" CRITICAL delivered after a restart
// could be actively misleading. It is logged instead, so the loss is visible.
func runRPCAlertWorker(ctx context.Context, chainID string, queue <-chan rpcJob) {
	for {
		select {
		case <-ctx.Done():
			drainAndLogAbandonedRPCJobs(chainID, queue)
			return
		default:
		}

		select {
		case <-ctx.Done():
			drainAndLogAbandonedRPCJobs(chainID, queue)
			return
		case job := <-queue:
			getRPCDispatch()(job)
		}
	}
}

// drainAndLogAbandonedRPCJobs empties queue without dispatching and logs how
// many jobs (if any) were still buffered when the worker stopped.
func drainAndLogAbandonedRPCJobs(chainID string, queue <-chan rpcJob) {
	dropped := 0
	for {
		select {
		case <-queue:
			dropped++
		default:
			if dropped > 0 {
				log.Printf("[rpc][%s] worker stopped with %d queued alert(s) still undelivered", chainID, dropped)
			}
			return
		}
	}
}

// NewRPCObserver builds the pool observer that turns endpoint transitions
// into operator-visible alerts, and starts the single worker goroutine (bound
// to ctx, so it stops when the chain's monitoring context is cancelled — e.g.
// an admin restart) that actually performs the dispatch.
//
// The observer itself only ever does fast, synchronous, in-memory work (the
// cooldown/pairing decision, building the message, a non-blocking channel
// send) — never network or DB I/O. rpcpool.Observer's contract requires this:
// it runs synchronously on whichever goroutine triggered the pool event,
// which can be the block-collection loop or an inbound admin/API request
// handler, and must not block. The slow part (SendInfoValidator +
// InsertAlertlog) always happens on the dedicated worker goroutine instead.
//
// A rotation is log-only: the pool recovered on its own and there is nothing
// for an operator to do. Losing every endpoint is a CRITICAL, because from
// that moment on no participation is recorded and no validator alert can
// fire.
func NewRPCObserver(ctx context.Context, db *gorm.DB, chainID string) rpcpool.Observer {
	queue := make(chan rpcJob, rpcAlertQueueSize)
	go runRPCAlertWorker(ctx, chainID, queue)

	enqueue := func(job rpcJob) bool {
		select {
		case queue <- job:
			return true
		default:
			return false
		}
	}

	return func(ev rpcpool.Event, endpoint string, err error) {
		switch ev {
		case rpcpool.EventRotated:
			log.Printf("[rpc][%s] failed over to endpoint %s", chainID, endpoint)

		case rpcpool.EventAllDown:
			cooldown := GetThresholds().RPCErrorCooldown()
			if !shouldSendRPCAlert(chainID, time.Now(), cooldown) {
				log.Printf("[rpc][%s] all endpoints down, alert suppressed by cooldown (%v)", chainID, cooldown)
				return
			}
			msg := fmt.Sprintf("[%s] 🚨 CRITICAL: every RPC endpoint is unreachable. Block collection and validator alerts are stopped. Last error: %v", chainID, err)
			job := rpcJob{
				db:      db,
				chainID: chainID,
				level:   "CRITICAL",
				msg:     msg,
				data: internal.AlertData{
					ChainID: chainID,
					Level:   internal.AlertCritical,
					Emoji:   "🚨",
					Title:   "RPC endpoints unreachable",
					Fields: []internal.AlertField{
						{Name: "last active endpoint", Value: endpoint},
						{Name: "error", Value: fmt.Sprintf("%v", err)},
						{Name: "impact", Value: "block collection and validator alerts are stopped"},
					},
				},
			}
			if !enqueue(job) {
				// Drop, never silently: clear the pairing so a later
				// recovery does not send an orphan RESOLVED for an alert
				// nobody actually received. The cooldown clock (lastDispatch)
				// is deliberately left alone — see unannounceRPCAlert.
				log.Printf("[rpc][%s] dropping CRITICAL alert: dispatch queue full", chainID)
				unannounceRPCAlert(chainID)
			}

		case rpcpool.EventRecovered:
			if !shouldSendRPCResolved(chainID) {
				log.Printf("[rpc][%s] recovery observed with no announced outage, nothing to resolve", chainID)
				return
			}
			msg := fmt.Sprintf("[%s] ✅ RPC connectivity restored on %s.", chainID, endpoint)
			job := rpcJob{
				db:      db,
				chainID: chainID,
				level:   "RESOLVED",
				msg:     msg,
				data: internal.AlertData{
					ChainID:     chainID,
					Level:       internal.AlertInfo,
					Emoji:       "✅",
					Title:       "RPC connectivity restored",
					Description: fmt.Sprintf("Serving from %s again.", endpoint),
				},
			}
			if !enqueue(job) {
				// Nothing to roll back here: shouldSendRPCResolved already
				// cleared "announced", so pairing state stays consistent —
				// this RESOLVED is simply lost, logged loudly rather than
				// silently.
				log.Printf("[rpc][%s] dropping RESOLVED alert: dispatch queue full", chainID)
			}
		}
	}
}
