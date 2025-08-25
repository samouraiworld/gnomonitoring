package govdao

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/gorilla/websocket"
	"github.com/machinebox/graphql"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
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

func GetMessageTitle(height int) error {
	URLgraphql := internal.Config.Graphql
	client := graphql.NewClient(URLgraphql)

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
	log.Printf("Blocks fetched: %+v\n", respData)

	// // Parcours les transactions et décode le content_raw
	// for _, block := range respData.GetBlocks {
	// 	for _, tx := range block.Txs {
	// 		// DecodeContentRaw(tx.ContentRaw)
	// 	}
	// }

	return nil
}

func FetchGovDAOEvents() ([]Transaction, error) {
	URLgraphql := "https://" + internal.Config.Graphql
	client := graphql.NewClient(URLgraphql)
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
					func
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

func WebsocketGovdao(db *gorm.DB) {
	wsURL := "wss://" + internal.Config.Graphql

	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		log.Fatal("Dial error:", err)
	}
	defer c.Close()

	initMsg := gqlMessage{
		Type: "connection_init",
	}
	c.WriteJSON(initMsg)

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
	c.WriteJSON(startMsg)

	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			log.Println("Read error:", err)
			return
		}
		log.Println("Message reçu:", string(message))

		var msg WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Println("JSON decode error:", err)
			continue
		}

		if msg.Type != "data" {
			// Ignore les "ka" ou autres messages
			continue
		}

		tx := msg.Payload.Data.GetTransactions
		ProcessProposal(tx, "socket", db)

	}

}
func ExtractTitle(proposalID int) (string, error) {
	rpcClient, err := rpcclient.NewHTTPClient(internal.Config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := &gnoclient.Client{RPCClient: rpcClient}

	proposalTitle, err := GnoQueryString(client, gnoclient.QueryCfg{
		Path: "vm/qeval",
		Data: fmt.Appendf(nil, "gno.land/r/gov/dao.proposals.GetProposal(%d).Title()", proposalID),
	})
	if err != nil {
		// do something with error
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
func GetTxsByBlockHeight(height int) (*TxBlock, error) {
	URLgraphql := "https://" + internal.Config.Graphql
	client := graphql.NewClient(URLgraphql)

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

	// Retrun tx

	return &respData.GetBlocks[0].Txs[0], nil
}
func InitGovdao(db *gorm.DB) {
	Trans, err := FetchGovDAOEvents()
	if err != nil {
		log.Printf("Error fetch govdao %s", err)
		return
	}
	for _, tx := range Trans {
		ProcessProposal(tx, "Fetch", db)
	}

}

func ProcessProposal(tx Transaction, who string, db *gorm.DB) {
	for _, ev := range tx.Response.Events {
		if ev.Type == "ProposalCreated" {
			for _, attr := range ev.Attrs {
				if attr.Key == "id" {
					// Construire l'URL de la proposition
					url := fmt.Sprintf("%s/r/gov/dao:%s", internal.Config.Gnoweb, attr.Value)

					// Convertir ID en int
					idInt, err := strconv.Atoi(attr.Value)
					if err != nil {
						log.Printf("Error converting id to int: %v", err)
						continue
					}

					// Récupération du titre
					title, err := ExtractTitle(idInt)
					if err != nil {
						log.Printf("Error fetching title: %v", err)
						continue
					}

					// Afficher Block Height
					log.Printf("Title: %s", title)
					log.Printf("Block Height: %d", tx.BlockHeight)

					// Récupérer le hash de la transaction associée
					txData, err := GetTxsByBlockHeight(tx.BlockHeight)
					if err != nil {
						log.Printf("Error fetching tx hash: %v", err)
						continue
					}

					txurl := fmt.Sprintf(
						"https://gnoscan.io/transactions/details?chainId=test7&txhash=%s",
						txData.Hash,
					)
					log.Printf("tx URL %s", txurl)

					// Insertion en base
					log.Printf("ID: %d", idInt)
					database.InsertGovdao(db, idInt, url, title, txurl)
					switch who {
					case "socket":
						internal.MultiSendReportGovdao(idInt, title, url, txurl, db)

					}

				}
			}
		}
	}
}

func StartGovDAo(db *gorm.DB) {

	InitGovdao(db)

	WebsocketGovdao(db)

}
