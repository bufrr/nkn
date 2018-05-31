package main

import (
	"errors"
	"flag"
	"math/rand"
	"os"
	"runtime"
	"time"

	"github.com/nknorg/nkn/consensus/dbft"
	"github.com/nknorg/nkn/consensus/ising"
	"github.com/nknorg/nkn/core/ledger"
	"github.com/nknorg/nkn/core/transaction"
	"github.com/nknorg/nkn/crypto"
	"github.com/nknorg/nkn/db"
	"github.com/nknorg/nkn/net"
	"github.com/nknorg/nkn/net/chord"
	"github.com/nknorg/nkn/net/protocol"
	"github.com/nknorg/nkn/por"
	"github.com/nknorg/nkn/rpc/httpjson"
	"github.com/nknorg/nkn/rpc/httprestful"
	"github.com/nknorg/nkn/util/config"
	"github.com/nknorg/nkn/util/log"
	"github.com/nknorg/nkn/wallet"
	"github.com/nknorg/nkn/websocket"
)

func init() {
	log.Init(log.Path, log.Stdout)
	runtime.GOMAXPROCS(runtime.NumCPU())
	rand.Seed(time.Now().UnixNano())
	crypto.SetAlg(config.Parameters.EncryptAlg)
}

func InitLedger(account *wallet.Account) error {
	var err error
	store, err := db.NewLedgerStore()
	if err != nil {
		return err
	}
	ledger.StandbyBookKeepers = wallet.GetBookKeepers(account)
	blockChain, err := ledger.NewBlockchainWithGenesisBlock(store, ledger.StandbyBookKeepers)
	if err != nil {
		return err
	}
	ledger.DefaultLedger = &ledger.Ledger{
		Blockchain: blockChain,
		Store:      store,
	}
	transaction.TxStore = ledger.DefaultLedger.Store

	return nil
}

func StartNetworking(pubKey *crypto.PubKey, ring *chord.Ring) protocol.Noder {
	node := net.StartProtocol(pubKey, ring)
	node.SyncNodeHeight()
	// node.WaitForFourPeersStart()
	node.WaitForSyncBlkFinish()

	rpcServer := httpjson.NewServer(node)
	go rpcServer.Start()
	httprestful.StartServer(node)

	return node
}

func StartConsensus(wallet wallet.Wallet, node protocol.Noder) {
	if protocol.SERVICENODENAME != config.Parameters.NodeType {
		switch config.Parameters.ConsensusType {
		case "ising":
			log.Info("ising consensus starting ...")
			account, _ := wallet.GetDefaultAccount()
			ising.StartIsingConsensus(account, node)
		case "dbft":
			log.Info("dbft consensus starting ...")
			dbftServices := dbft.NewDbftService(wallet, "logdbft", node)
			go dbftServices.Start()
		}
	}
}

func nknMain() error {
	log.Trace("Node version: ", config.Version)
	var name = flag.String("test", "", "usage")
	flag.Parse()

	var ring *chord.Ring
	var transport *chord.TCPTransport
	var err error

	// Start the Chord ring testing process
	if *name != "" {
		//flag.PrintDefaults()
		if *name == "create" {
			ring, transport, err = chord.CreateNet()
		} else if *name == "join" {
			config.IncrementPort()
			ring, transport, err = chord.JoinNet()
		}

		if err != nil {
			return err
		}

		defer transport.Shutdown()
		defer ring.Shutdown()
	}

	// Get local account
	wallet := wallet.GetWallet()
	if wallet == nil {
		return errors.New("open local wallet error")
	}
	account, err := wallet.GetDefaultAccount()
	if err != nil {
		return errors.New("load local account error")
	}

	// initialize ledger
	err = InitLedger(account)
	if err != nil {
		return errors.New("ledger initialization error")
	}
	// if InitLedger return err, ledger.DefaultLedger is uninitialized.
	defer ledger.DefaultLedger.Store.Close()

	err = por.InitPorServer(account)
	if err != nil {
		return errors.New("PorServer initialization error")
	}

	// start P2P networking
	node := StartNetworking(account.PublicKey, ring)

	// start relay service
	node.StartRelayer(account)

	// start websocket server
	websocket.StartServer(node)

	httpjson.Wallet = wallet

	// start consensus
	StartConsensus(wallet, node)

	for {
		time.Sleep(dbft.GenBlockTime)
		if log.CheckIfNeedNewFile() {
			log.ClosePrintLog()
			log.Init(log.Path, os.Stdout)
		}
	}
}

func main() {
	// Call the nknMain so the defers will be executed in the case of a graceful shutdown.
	if err := nknMain(); err != nil {
		os.Exit(1)
	}
}
