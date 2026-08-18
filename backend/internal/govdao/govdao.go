package govdao

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/gorilla/websocket"
	"github.com/machinebox/graphql"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gorm.io/gorm"
)

// clientForChain returns a gnoclient backed by the chain's shared RPC pool,
// so every GovDAO query inherits the same endpoint failover as validator
// monitoring instead of hardcoding RPCEndpoints[0].
func clientForChain(chainID string) (*gnoclient.Client, error) {
	pool, ok := rpcpool.Get(chainID)
	if !ok {
		return nil, fmt.Errorf("no RPC pool registered for chain %q", chainID)
	}
	return &gnoclient.Client{RPCClient: pool}, nil
}

type TxBlock struct {
	Hash string `json:"hash"`
}

type Block struct {
	Txs []TxBlock `json:"txs"`
}

type GetBlocksResponse struct {
	GetBlocks []Block `json:"getBlocks"`
}
type gqlMessage struct {
	ID      string      `json:"id,omitempty"`
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}
type Attr struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type GnoEvent struct {
	Type  string `json:"type"`
	Attrs []Attr `json:"attrs"`
}

type Response struct {
	Events []GnoEvent `json:"events"`
}

type Transaction struct {
	BlockHeight int      `json:"block_height"`
	Index       int      `json:"index"`
	Response    Response `json:"response"`
}

type PayloadData struct {
	GetTransactions Transaction `json:"getTransactions"`
}

type Payload struct {
	Data PayloadData `json:"data"`
}

type WSMessage struct {
	ID      string  `json:"id,omitempty"`
	Type    string  `json:"type"`
	Payload Payload `json:"payload,omitempty"`
}
type Proposal struct {
	ID     int
	Status string
	Title  string
	Url    string
	TxUrl  string
}

func GetMessageTitle(height int, graphqlEndpoint string) error {
	client := graphql.NewClient(graphqlEndpoint)

	req := graphql.NewRequest(`
        query getSpecificBlocksByHeight($height: Int!) {
            getBlocks(
                where: {
                    height: { eq: $height }
                }
            ) {
                txs {
                    content_raw
                }
            }
        }
    `)

	// Injecter la variable height
	req.Var("height", height)

	var respData GetBlocksResponse
	if err := client.Run(context.Background(), req, &respData); err != nil {
		return err
	}

	// Log la réponse brute

	// // Parcours les transactions et décode le content_raw
	// for _, block := range respData.GetBlocks {
	// 	for _, tx := range block.Txs {
	// 		// DecodeContentRaw(tx.ContentRaw)
	// 	}
	// }

	return nil
}

func FetchGovDAOEvents(graphqlEndpoints []string) ([]Transaction, error) {
	var lastErr error
	for _, endpoint := range graphqlEndpoints {
		client := graphql.NewClient(endpoint)
		req := graphql.NewRequest(`
			query getEvents {
			getTransactions(
				where: {
				# Only show transactions that succeeded.
				success: {eq: true},
				response: {
					events: {

					# This filter is checking that all transactions will contains a GnoEvent that
					# is GNOSWAP type calling SetPoolCreationFee function.
					GnoEvent: {
						type: { eq:"ProposalCreated" }
						pkg_path: {eq: "gno.land/r/gov/dao"}
					}
					}
				}
				}
			) {
				block_height
				index
				response {
				events {
					... on GnoEvent {
					type

					attrs {
						key
						value


					}
					}
				}
				}
			}
			}
  `)
		var respData struct {
			GetTransactions []Transaction `json:"getTransactions"`
		}
		if err := client.Run(context.Background(), req, &respData); err != nil {
			log.Printf("[govdao] FetchGovDAOEvents endpoint %s failed: %v", endpoint, err)
			lastErr = err
			continue
		}
		return respData.GetTransactions, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no GraphQL endpoints configured")
}

func ExtractGovDAOIDs(txs []Transaction) []string {
	ids := []string{}
	for _, tx := range txs {
		for _, ev := range tx.Response.Events {
			if ev.Type == "ProposalCreated" {
				for _, attr := range ev.Attrs {
					if attr.Key == "id" {
						ids = append(ids, attr.Value)
					}
				}
			}
		}
	}
	return ids
}

func WebsocketGovdao(ctx context.Context, db *gorm.DB, chainID string, graphqlEndpoints []string, client *gnoclient.Client, gnowebEndpoint string) {
	primaryGraphQL := ""
	if len(graphqlEndpoints) > 0 {
		primaryGraphQL = graphqlEndpoints[0]
	}
	wsURL := strings.Replace(primaryGraphQL, "http", "ws", 1)

	const (
		backoffMin = 2 * time.Second
		backoffMax = 60 * time.Second
	)
	backoff := backoffMin

	for {
		select {
		case <-ctx.Done():
			log.Printf("[govdao][%s] WebsocketGovdao stopped", chainID)
			return
		default:
		}

		c, wsResp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			if wsResp != nil {
				wsResp.Body.Close()
			}
			log.Printf("[govdao][%s] dial error: %v — retrying in %s", chainID, err, backoff)
			select {
			case <-ctx.Done():
				log.Printf("[govdao][%s] WebsocketGovdao stopped during backoff", chainID)
				return
			case <-time.After(backoff):
			}
			if backoff < backoffMax {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
			continue
		}

		// Close the WebSocket connection when the context is cancelled.
		go func(conn *websocket.Conn) {
			<-ctx.Done()
			conn.Close()
		}(c)

		// Successful connection — reset backoff.
		backoff = backoffMin

		initMsg := gqlMessage{
			Type: "connection_init",
		}
		if err := c.WriteJSON(initMsg); err != nil {
			log.Printf("[govdao][%s] send init message failed: %v", chainID, err)
		}

		query := `
        subscription {
          getTransactions(
            where: {
              success: {eq: true},
              response: {
                events: {
                  GnoEvent: {
                    type: { eq:"ProposalCreated" }
                    pkg_path: {eq: "gno.land/r/gov/dao"}
                  }
                }
              }
            }
          ) {
            block_height
            index
            response {
              events {
                ... on GnoEvent {
                  type
                  attrs { key value }
                }
              }
            }
          }
        }
    `
		startMsg := gqlMessage{
			ID:   "1",
			Type: "start",
			Payload: map[string]interface{}{
				"query": query,
			},
		}
		if err := c.WriteJSON(startMsg); err != nil {
			log.Printf("[govdao][%s] send start message failed: %v", chainID, err)
		}

		readErr := false
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Printf("[govdao][%s] websocket read error: %v", chainID, err)
				readErr = true
				break
			}

			var msg WSMessage
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Printf("[govdao][%s] JSON decode error: %v", chainID, err)
				continue
			}

			if msg.Type != "data" {

				continue
			}

			tx := msg.Payload.Data.GetTransactions
			ProcessProposal(tx, "socket", db, chainID, graphqlEndpoints, client, gnowebEndpoint)
		}

		c.Close()
		if readErr {
			log.Printf("[govdao][%s] connection lost — retrying in %s", chainID, backoff)
			time.Sleep(backoff)
			if backoff < backoffMax {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
		}
	}

}
func ExtractTitle(proposalID int, client *gnoclient.Client) (string, error) {
	proposalTitle, err := GnoQueryString(client, gnoclient.QueryCfg{
		Path: "vm/qeval",
		Data: fmt.Appendf(nil, "gno.land/r/gov/dao.proposals.GetProposal(%d).Title()", proposalID),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get proposal title: %w", err)
	}

	return proposalTitle, nil
}
func GnoQueryString(client *gnoclient.Client, cfg gnoclient.QueryCfg) (string, error) {
	queryResult, err := client.Query(cfg)
	if err != nil {
		return "", fmt.Errorf("query %q: %w", cfg.Path+":"+string(cfg.Data), err)
	}
	if queryResult.Response.Error != nil {
		return "", fmt.Errorf("query %q: returned error: %w", cfg.Path+":"+string(cfg.Data), err)
	}
	res, err := parseGnoStringResponse(queryResult.Response.Data)
	if err != nil {
		return "", fmt.Errorf("query %q: parse string in response %q: %w", cfg.Path+":"+string(cfg.Data), string(queryResult.Response.Data), err)
	}
	return res, nil
}

func parseGnoStringResponse(bz []byte) (string, error) {
	s := string(bz)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, " string)")
	return strconv.Unquote(s)
}

func GetTxsByBlockHeight(height int, graphqlEndpoints []string) (*TxBlock, error) {
	var lastErr error
	for _, endpoint := range graphqlEndpoints {
		client := graphql.NewClient(endpoint)

		req := graphql.NewRequest(`
		query getSpecificBlocksByHeight($height: Int!) {
			getBlocks(
				where: { height: { eq: $height } }
			) {
				txs {
					hash
				}
			}
		}
	`)

		req.Var("height", height)

		var respData GetBlocksResponse
		if err := client.Run(context.Background(), req, &respData); err != nil {
			log.Printf("[govdao] GetTxsByBlockHeight endpoint %s failed: %v", endpoint, err)
			lastErr = err
			continue
		}

		if len(respData.GetBlocks) == 0 || len(respData.GetBlocks[0].Txs) == 0 {
			return nil, fmt.Errorf("no transactions found for block %d", height)
		}

		return &respData.GetBlocks[0].Txs[0], nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("GraphQL query error: %w", lastErr)
	}
	return nil, fmt.Errorf("no GraphQL endpoints configured")
}

func InitGovdao(db *gorm.DB, chainID string, graphqlEndpoints []string, client *gnoclient.Client, gnowebEndpoint string) {
	Trans, err := FetchGovDAOEvents(graphqlEndpoints)
	if err != nil {
		log.Printf("[govdao][%s] init fetch failed: %v", chainID, err)
		return
	}
	for _, tx := range Trans {
		ProcessProposal(tx, "Fetch", db, chainID, graphqlEndpoints, client, gnowebEndpoint)
	}

}

// Proposal enrichment fetchers, declared as package variables so tests can
// override them without a live RPC/GraphQL endpoint.
var (
	fetchProposalTitle  = ExtractTitle
	fetchProposalStatus = ExtractProposalRender
	fetchTxByHeight     = GetTxsByBlockHeight

	// fetchChainProposalStatus resolves a proposal's current on-chain status
	// from its chain ID alone. CheckProposalStatus polls proposals across all
	// chains, so it needs the client resolution folded in.
	fetchChainProposalStatus = chainProposalStatus

	// notifyProposalStatus announces a proposal reaching a terminal status.
	notifyProposalStatus = sendProposalStatusNotification
)

// chainProposalStatus reads proposalID's current status from chainID's pooled
// RPC client.
func chainProposalStatus(chainID string, proposalID int) (string, error) {
	client, err := clientForChain(chainID)
	if err != nil {
		return "", err
	}
	return fetchProposalStatus(proposalID, client)
}

func ProcessProposal(tx Transaction, who string, db *gorm.DB, chainID string, graphqlEndpoints []string, client *gnoclient.Client, gnowebEndpoint string) {
	for _, ev := range tx.Response.Events {
		if ev.Type != "ProposalCreated" {
			continue
		}
		for _, attr := range ev.Attrs {
			if attr.Key != "id" {
				continue
			}

			// Build Url
			url := fmt.Sprintf("%s/r/gov/dao:%s", gnowebEndpoint, attr.Value)

			// Convert ID to Int — the only field we cannot proceed without.
			idInt, err := strconv.Atoi(attr.Value)
			if err != nil {
				log.Printf("[govdao][%s] error parsing proposal ID %q: %v", chainID, attr.Value, err)
				continue
			}

			// Title/status/tx enrichment is best-effort. Some realm versions
			// (e.g. test-13) don't expose the same `proposals.GetProposal(id)`
			// accessor as betanet, so the qeval query can return an error or
			// panic. A failed enrichment must NOT drop the proposal: the
			// websocket delivers each ProposalCreated event only once, so an
			// aborted proposal is never inserted nor announced. Fall back to
			// sane defaults and still insert + notify.
			title, err := fetchProposalTitle(idInt, client)
			if err != nil {
				log.Printf("[govdao][%s] title unavailable for proposal %d, using fallback: %v", chainID, idInt, err)
				title = fmt.Sprintf("Proposal #%d", idInt)
			}

			// statusSynced records whether the stored status actually came
			// from the chain. A failed query, or a render the parser could
			// not read, leaves the proposal open to silent reconciliation on
			// a later watcher pass instead of being treated as confirmed.
			status, err := fetchProposalStatus(idInt, client)
			if err != nil {
				log.Printf("[govdao][%s] status unavailable for proposal %d: %v", chainID, idInt, err)
				status = StatusUnknown
			}
			statusSynced := err == nil && status != StatusUnknown

			txurl := ""
			if txData, err := fetchTxByHeight(tx.BlockHeight, graphqlEndpoints); err != nil {
				log.Printf("[govdao][%s] tx hash unavailable for proposal %d (block %d): %v", chainID, idInt, tx.BlockHeight, err)
			} else {
				txurl = fmt.Sprintf("https://gnoscan.io/transactions/details?txhash=%s", txData.Hash)
			}

			// Insert to db
			if err := database.InsertGovdao(db, idInt, chainID, url, title, txurl, status, statusSynced); err != nil {
				log.Printf("[govdao][%s] InsertGovdao error: %v", chainID, err)
			}
			if who == "socket" {
				if err := internal.MultiSendReportGovdao(chainID, idInt, title, url, txurl, db); err != nil {
					log.Printf("[govdao][%s] MultiSendReportGovdao error: %v", chainID, err)
				}
			}
		}
	}
}

// =========================================== Extract Status
func GnoQueryRender(client *gnoclient.Client, cfg gnoclient.QueryCfg) (string, error) {
	res, err := client.Query(cfg)
	if err != nil {
		return "", err
	}

	return string(res.Response.Data), nil
}

// Proposal statuses stored in govdaos.status. ACCEPTED and REJECTED are the
// two terminal states; IN PROGRESS means the proposal is still open for votes;
// UNKNOWN means the render could not be read and carries no information about
// the proposal at all.
const (
	StatusAccepted   = "ACCEPTED"
	StatusRejected   = "REJECTED"
	StatusInProgress = "IN PROGRESS"
	StatusUnknown    = "UNKNOWN"
)

// Marker lines emitted by gno.land/r/gov/dao/v3/impl's proposalStatus.String()
// on a single-proposal page. Exactly one of them is present per render.
const (
	markerAccepted   = "**PROPOSAL HAS BEEN ACCEPTED**"
	markerDenied     = "**PROPOSAL HAS BEEN DENIED**"
	markerInProgress = "**Proposal is open for votes**"
)

// isTerminalProposalStatus reports whether s is a final on-chain outcome, i.e.
// one worth notifying about. A proposal never leaves a terminal state.
func isTerminalProposalStatus(s string) bool {
	return s == StatusAccepted || s == StatusRejected
}

// parseProposalStatus reads the proposal status out of a gov/dao proposal page
// render.
//
// It matches only the explicit status marker lines the realm emits. Matching
// anything looser is unsafe here: the page also renders the proposal's own
// description (which may contain the word "ACCEPTED") and an action bar with
// "Vote YES"/"Vote NO" links that the realm emits unconditionally — including
// on already accepted and denied proposals.
//
// An unrecognised render yields StatusUnknown, never a terminal state: failing
// to read the page tells us nothing about the proposal, and treating that as a
// rejection would fabricate an alert out of an RPC hiccup or a realm upgrade.
func parseProposalStatus(render string) string {
	for _, line := range strings.Split(render, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.Contains(trimmed, markerAccepted):
			return StatusAccepted
		case strings.Contains(trimmed, markerDenied):
			return StatusRejected
		case strings.Contains(trimmed, markerInProgress):
			return StatusInProgress
		}
	}
	return StatusUnknown
}

func ExtractProposalRender(proposalID int, client *gnoclient.Client) (string, error) {
	data := fmt.Sprintf("gno.land/r/gov/dao:%d", proposalID)
	res, err := GnoQueryRender(client, gnoclient.QueryCfg{
		Path: "vm/qrender",
		Data: []byte(data),
	})
	if err != nil {
		return "", err
	}

	return parseProposalStatus(res), nil
}

// terminalStatusDisplay carries the per-status presentation of a terminal
// outcome, so the ACCEPTED and REJECTED notifications share one code path.
var terminalStatusDisplay = map[string]struct {
	emoji string
	verb  string
}{
	StatusAccepted: {emoji: "✅", verb: "accepted"},
	StatusRejected: {emoji: "❌", verb: "rejected"},
}

// sendProposalStatusNotification announces that p reached the terminal status
// status, on Discord/Slack and on Telegram.
func sendProposalStatusNotification(db *gorm.DB, p database.Govdao, status string) {
	display := terminalStatusDisplay[status]

	msg := fmt.Sprintf("--- \n 🗳️"+
		"Proposal N° %d: %s  -  \n"+
		" 🔗source: %s \n "+
		" %s",
		p.Id, p.Title, p.Url, status)
	if err := internal.SendInfoGovdao(p.ChainID, msg, db); err != nil {
		log.Printf("[govdao] SendInfoGovdao error: %v", err)
	}

	msgT := fmt.Sprintf(
		"🗳️ [%s] <b>%s Proposal Nº %d</b>: %s\n"+
			"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
			"<b>%s</b>\n",
		p.ChainID,
		display.emoji,
		p.Id,
		p.Title,
		p.Url,
		status,
	)
	if err := telegram.MsgTelegram(msgT, internal.Config.TokenTelegramGovdao, "govdao", db); err != nil {
		log.Printf("[govdao] MsgTelegram error: %v", err)
	}
}

// storeProposalStatus persists a proposal's status. govdaos is keyed on
// (id, chain_id), so the WHERE clause must carry both: proposal #0 exists
// independently on every chain.
func storeProposalStatus(db *gorm.DB, p database.Govdao, status string) {
	if err := db.Model(&database.Govdao{}).
		Where("id = ? AND chain_id = ?", p.Id, p.ChainID).
		Updates(map[string]any{"status": status, "status_synced": true}).Error; err != nil {
		log.Printf("[govdao][%s] failed to update proposal %d status: %v", p.ChainID, p.Id, err)
	}
}

// CheckProposalStatus polls every known proposal's on-chain status and
// announces the ones that just reached a terminal outcome.
//
// A proposal whose stored status was never confirmed against the chain by the
// current parser (status_synced = false) is reconciled silently: its status is
// corrected in place with no notification. Rows predating the parser fix all
// land here, because the old parser read every rejected proposal as
// "IN PROGRESS" — announcing those would flood every channel with rejections
// of months-old proposals on the first run after deploy.
func CheckProposalStatus(db *gorm.DB) {
	var govdao []database.Govdao
	if err := db.Find(&govdao).Error; err != nil {
		log.Printf("[govdao] error fetching proposals: %v", err)
		return
	}

	for _, p := range govdao {
		currentStatus, err := fetchChainProposalStatus(p.ChainID, p.Id)
		if err != nil {
			log.Printf("[govdao][%s] error fetching status for proposal %d: %v", p.ChainID, p.Id, err)
			continue
		}
		// An unreadable render says nothing about the proposal. Never let it
		// overwrite a known status, and never let it stand in for a rejection.
		if currentStatus == StatusUnknown {
			log.Printf("[govdao][%s] unreadable render for proposal %d, leaving status %q untouched",
				p.ChainID, p.Id, p.Status)
			continue
		}

		if !p.StatusSynced {
			if currentStatus != p.Status {
				log.Printf("[govdao][%s] reconciling proposal %d: %q -> %q (no notification)",
					p.ChainID, p.Id, p.Status, currentStatus)
			}
			storeProposalStatus(db, p, currentStatus)
			continue
		}

		if currentStatus == p.Status {
			continue
		}

		if isTerminalProposalStatus(currentStatus) {
			log.Printf("[govdao][%s] proposal %d (%s) %s",
				p.ChainID, p.Id, p.Title, terminalStatusDisplay[currentStatus].verb)
			notifyProposalStatus(db, p, currentStatus)
		}
		storeProposalStatus(db, p, currentStatus)
	}
}

func StartProposalWatcher(db *gorm.DB) {
	ticker := time.NewTicker(5 * time.Minute)

	defer ticker.Stop()

	for {
		<-ticker.C
		log.Printf("[govdao] checking proposal statuses")
		CheckProposalStatus(db)
	}
}

func StartGovDAo(ctx context.Context, db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	client, err := clientForChain(chainID)
	if err != nil {
		log.Printf("[govdao][%s] %v; GovDAO watcher not started", chainID, err)
		return
	}
	InitGovdao(db, chainID, chainCfg.GraphqlEndpoints, client, chainCfg.GnowebEndpoint())
	WebsocketGovdao(ctx, db, chainID, chainCfg.GraphqlEndpoints, client, chainCfg.GnowebEndpoint())
}
