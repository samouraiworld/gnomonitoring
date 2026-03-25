package govdao

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/gorilla/websocket"
	"github.com/machinebox/graphql"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gorm.io/gorm"
)

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

func FetchGovDAOEvents(graphqlEndpoint string) ([]Transaction, error) {
	client := graphql.NewClient(graphqlEndpoint)
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
		return nil, err
	}
	return respData.GetTransactions, nil

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

func WebsocketGovdao(db *gorm.DB, chainID string, graphqlEndpoint string, rpcEndpoint string, gnowebEndpoint string) {
	wsURL := strings.Replace(graphqlEndpoint, "http", "ws", 1)

	const (
		backoffMin = 2 * time.Second
		backoffMax = 60 * time.Second
	)
	backoff := backoffMin

	for {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[govdao][%s] dial error: %v — retrying in %s", chainID, err, backoff)
			time.Sleep(backoff)
			if backoff < backoffMax {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
			continue
		}

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
			ProcessProposal(tx, "socket", db, chainID, graphqlEndpoint, rpcEndpoint, gnowebEndpoint)
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
func ExtractTitle(proposalID int, rpcEndpoint string) (string, error) {
	rpcClient, err := rpcclient.NewHTTPClient(rpcEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := &gnoclient.Client{RPCClient: rpcClient}

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

func GetTxsByBlockHeight(height int, graphqlEndpoint string) (*TxBlock, error) {
	client := graphql.NewClient(graphqlEndpoint)

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
		return nil, fmt.Errorf("GraphQL query error: %w", err)
	}

	// Check if a tx
	if len(respData.GetBlocks) == 0 || len(respData.GetBlocks[0].Txs) == 0 {
		return nil, fmt.Errorf("no transactions found for block %d", height)
	}

	// Return tx

	return &respData.GetBlocks[0].Txs[0], nil
}

func InitGovdao(db *gorm.DB, chainID string, graphqlEndpoint string, rpcEndpoint string, gnowebEndpoint string) {
	Trans, err := FetchGovDAOEvents(graphqlEndpoint)
	if err != nil {
		log.Printf("[govdao][%s] init fetch failed: %v", chainID, err)
		return
	}
	for _, tx := range Trans {
		ProcessProposal(tx, "Fetch", db, chainID, graphqlEndpoint, rpcEndpoint, gnowebEndpoint)
	}

}

func ProcessProposal(tx Transaction, who string, db *gorm.DB, chainID string, graphqlEndpoint string, rpcEndpoint string, gnowebEndpoint string) {
	for _, ev := range tx.Response.Events {
		if ev.Type == "ProposalCreated" {
			for _, attr := range ev.Attrs {
				if attr.Key == "id" {
					// Build Url
					url := fmt.Sprintf("%s/r/gov/dao:%s", gnowebEndpoint, attr.Value)

					// Convert ID to Int
					idInt, err := strconv.Atoi(attr.Value)
					if err != nil {
						log.Printf("[govdao][%s] error parsing proposal ID: %v", chainID, err)
						continue
					}

					// Get Title
					title, err := ExtractTitle(idInt, rpcEndpoint)
					if err != nil {
						log.Printf("[govdao][%s] error fetching title: %v", chainID, err)
						continue
					}

					status, err := ExtractProposalRender(idInt, rpcEndpoint)
					if err != nil {
						log.Printf("[govdao][%s] error fetching status: %v", chainID, err)
						continue
					}

					// Show block height

					// Get hash of transaction
					txData, err := GetTxsByBlockHeight(tx.BlockHeight, graphqlEndpoint)
					if err != nil {
						log.Printf("Error fetching tx hash: %v", err)
						continue
					}

					txurl := fmt.Sprintf(
						"https://gnoscan.io/transactions/details?txhash=%s",
						txData.Hash,
					)

					// Insert to db
					if err := database.InsertGovdao(db, idInt, chainID, url, title, txurl, status); err != nil {
						log.Printf("[govdao][%s] InsertGovdao error: %v", chainID, err)
					}
					if who == "socket" {
						if err := internal.MultiSendReportGovdao(idInt, title, url, txurl, db); err != nil {
							log.Printf("[govdao][%s] MultiSendReportGovdao error: %v", chainID, err)
						}
					}

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

func ExtractProposalRender(proposalID int, rpcEndpoint string) (string, error) {

	rpcClient, err := rpcclient.NewHTTPClient(rpcEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := &gnoclient.Client{RPCClient: rpcClient}

	data := fmt.Sprintf("gno.land/r/gov/dao:%d", proposalID)
	res, err := GnoQueryRender(client, gnoclient.QueryCfg{
		Path: "vm/qrender",
		Data: []byte(data),
	})
	if err != nil {
		return "", err
	}

	switch {
	case strings.Contains(res, "ACCEPTED"):
		return "ACCEPTED", nil
	case strings.Contains(res, "ACTIVE"):
		return "ACTIVE", nil
	case strings.Contains(res, "Vote YES"):
		return "IN PROGRESS", nil
	default:
		return "REJECTED", nil
	}
}

func CheckProposalStatus(db *gorm.DB) {
	var govdao []database.Govdao
	if err := db.Find(&govdao).Error; err != nil {
		log.Printf("[govdao] error fetching proposals: %v", err)
		return
	}

	for _, p := range govdao {
		chainCfg, err := internal.Config.GetChainConfig(p.ChainID)
		if err != nil {
			log.Printf("[govdao] unknown chain %q for proposal %d: %v", p.ChainID, p.Id, err)
			continue
		}
		currentStatus, err := ExtractProposalRender(p.Id, chainCfg.RPCEndpoint)
		if err != nil {
			log.Printf("[govdao][%s] error fetching status for proposal %d: %v", p.ChainID, p.Id, err)
			continue
		}

		if currentStatus == "ACCEPTED" && p.Status != "ACCEPTED" {
			log.Printf("[govdao] proposal %d (%s) accepted", p.Id, p.Title)

			// Send notification
			msg := fmt.Sprintf("--- \n 🗳️"+
				"Proposal N° %d: %s  -  \n"+
				" 🔗source: %s \n "+
				" ACCEPTED",
				p.Id, p.Title, p.Url)
			if err := internal.SendInfoGovdao(msg, db); err != nil {
				log.Printf("[govdao] SendInfoGovdao error: %v", err)
			}

			// Send Telegram message
			msgT := fmt.Sprintf(
				"🗳️ <b>✅ Proposal Nº %d</b>: %s\n"+
					"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
					"<b>ACCEPTED</b>\n",
				p.Id,
				p.Title,
				p.Url,
			)
			if err := telegram.MsgTelegram(msgT, internal.Config.TokenTelegramGovdao, "govdao", db); err != nil {
				log.Printf("[govdao] MsgTelegram error: %v", err)
			}

			// update GovDao (explicit WHERE to handle id=0)
			if err := db.Model(&database.Govdao{}).
				Where("id = ?", p.Id).
				Update("status", "ACCEPTED").Error; err != nil {
				log.Printf("[govdao] failed to update proposal %d status: %v", p.Id, err)
			}
		}
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

func StartGovDAo(db *gorm.DB, chainID string, graphqlEndpoint string, rpcEndpoint string, gnowebEndpoint string) {
	InitGovdao(db, chainID, graphqlEndpoint, rpcEndpoint, gnowebEndpoint)
	WebsocketGovdao(db, chainID, graphqlEndpoint, rpcEndpoint, gnowebEndpoint)
}
