	package monitoring
	import {

		
	}
	internal.LoadConfig()

	prometheus.MustRegister(missedBlocks)
	prometheus.MustRegister(consecutiveMissedBlocks)

	rpcClient, err := rpcclient.NewHTTPClient(config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}

	client := gnoclient.Client{RPCClient: rpcClient}

	go func() {
		missedTotal := 0
		consecutive := 0
		var previousHeight int64 = 0

		for {
			height, err := client.LatestBlockHeight()
			if err != nil {
				log.Println("Error fetching latest height:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			if height == previousHeight {
				time.Sleep(1 * time.Second)
				continue
			}
			previousHeight = height

			block, err := client.Block(height)
			if err != nil {
				log.Println("Error fetching block:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			found := false
			for _, precommit := range block.Block.LastCommit.Precommits {
				if precommit != nil && precommit.ValidatorAddress.String() == config.ValidatorAddress {
					found = true
					break
				}
			}

			if found {
				consecutive = 0
			} else {
				missedTotal++
				consecutive++
				log.Printf("Validator missed block %d\n", height)
			}

			missedBlocks.Set(float64(missedTotal))
			consecutiveMissedBlocks.Set(float64(consecutive))

			time.Sleep(1 * time.Second)
		}
	}()