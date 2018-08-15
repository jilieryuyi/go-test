package main

import (
	"flag"
	"fmt"
	log "github.com/sirupsen/logrus"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/thrasher-/gocryptotrader/common"
	"github.com/thrasher-/gocryptotrader/communications"
	"github.com/thrasher-/gocryptotrader/config"
	"github.com/thrasher-/gocryptotrader/currency"
	"github.com/thrasher-/gocryptotrader/currency/forexprovider"
	"github.com/thrasher-/gocryptotrader/exchanges"
	"github.com/thrasher-/gocryptotrader/portfolio"
	"path/filepath"
	"time"
	"github.com/thrasher-/gocryptotrader/exchanges/orderbook"
)

// Bot contains configuration, portfolio, exchange & ticker data and is the
// overarching type across this code base.
type Bot struct {
	config     *config.Config
	portfolio  *portfolio.Base
	exchanges  []exchange.IBotExchange
	comms      *communications.Communications
	shutdown   chan bool
	dryRun     bool
	configFile string
}

var bot Bot

func getWorkingPath() string {
	wd, err := os.Getwd()
	if err == nil {
		workingDir := filepath.ToSlash(wd) + "/"
		return workingDir
	}
	return "/"
}

func init() {
	log.SetFormatter(&log.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		ForceColors:      true,
		QuoteEmptyFields: true,
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
}

func main() {
	bot.shutdown = make(chan bool)
	HandleInterrupt()

	defaultPath, err := config.GetFilePath(getWorkingPath()+"config.json")
	if err != nil {
		log.Fatal(err)
	}

	//Handle flags
	flag.StringVar(&bot.configFile, "config", defaultPath, "config file to load")
	dryrun := flag.Bool("dryrun", false, "dry runs bot, doesn't save config file")
	version := flag.Bool("version", false, "retrieves current GoCryptoTrader version")
	flag.Parse()

	if *version {
		fmt.Printf(BuildVersion(true))
		os.Exit(0)
	}

	if *dryrun {
		bot.dryRun = true
	}

	bot.config = &config.Cfg
	fmt.Println(BuildVersion(false))
	log.Printf("load config file %s", bot.configFile)

	err = bot.config.LoadConfig(bot.configFile)
	if err != nil {
		log.Fatalf("bot.config.LoadConfig fail, error=[%s]", err)
		return 
	}

	AdjustGoMaxProcs()
	log.Printf("Bot '%s' started", bot.config.Name)
	log.Printf("Bot dry run mode: %v", common.IsEnabled(bot.dryRun))

	log.Printf("Available Exchanges: %d. Enabled Exchanges: %d.",
		len(bot.config.Exchanges),
		bot.config.CountEnabledExchanges())

	common.HTTPClient = common.NewHTTPClientWithTimeout(bot.config.GlobalHTTPTimeout)
	log.Printf("Global HTTP request timeout: %v.", common.HTTPClient.Timeout)

	SetupExchanges()
	if len(bot.exchanges) == 0 {
		log.Fatalf("No exchanges were able to be loaded. Exiting")
	}

	log.Println("Starting communication mediums..")
	bot.comms = communications.NewComm(bot.config.GetCommunicationsConfig())
	bot.comms.GetEnabledCommunicationMediums()

	log.Printf("Fiat display currency: %s.", bot.config.Currency.FiatDisplayCurrency)
	currency.BaseCurrency = bot.config.Currency.FiatDisplayCurrency
	currency.FXProviders = forexprovider.StartFXService(bot.config.GetCurrencyConfig().ForexProviders)
	log.Printf("Primary forex conversion provider: %s.", bot.config.GetPrimaryForexProvider())
	err = bot.config.RetrieveConfigCurrencyPairs(true)
	if err != nil {
		log.Fatalf("Failed to retrieve config currency pairs. Error: %s", err)
	}
	log.Println("Successfully retrieved config currencies.")
	log.Println("Fetching currency data from forex provider..")
	err = currency.SeedCurrencyData(common.JoinStrings(currency.FiatCurrencies, ","))
	if err != nil {
		log.Fatalf("Unable to fetch forex data. Error: %s", err)
	}

	bot.portfolio = &portfolio.Portfolio
	bot.portfolio.SeedPortfolio(bot.config.Portfolio)
	SeedExchangeAccountInfo(GetAllEnabledExchangeAccountInfo().Data)

	//go portfolio.StartPortfolioWatcher()
	//go TickerUpdaterRoutine()
	
	// Create a session which maintains a pool of socket connections
	// to our MongoDB.
	//session, err := mgo.Dial("127.0.0.1:27017")

	mongo := NewMongoDb("127.0.0.1:27017")
	defer mongo.Close()

	//if err != nil {
	//	fmt.Printf("Can't connect to mongo, go error %v", err)
	//	os.Exit(1)
	//}

	//defer session.Close()

	// SetSafe changes the session safety mode.
	// If the safe parameter is nil, the session is put in unsafe mode, and writes become fire-and-forget,
	// without error checking. The unsafe mode is faster since operations won't hold on waiting for a confirmation.
	// http://godoc.org/labix.org/v2/mgo#Session.SetMode.
	//session.SetSafe(&mgo.Safe{})

	// get collection
	//collection := session.DB("OrderBooks").C("orderbooks")
	//log.Printf("%+v", collection)

	// 创建一个集合
	col := mongo.NewCollection("OrderBooks", "orderbooks")

	go OrderbookUpdaterRoutine(func(exchangeName, ticker string, lastUpdated time.Time,
		asks, bids []orderbook.Item,) {
		// 异步写入
		mongo.AsyncInsert(col, exchangeName, ticker, lastUpdated, asks, bids)
	})

	if bot.config.Webserver.Enabled {
		listenAddr := bot.config.Webserver.ListenAddress
		log.Printf(
			"HTTP Webserver support enabled. Listen URL: http://%s:%d/",
			common.ExtractHost(listenAddr), common.ExtractPort(listenAddr),
		)

		router := NewRouter(bot.exchanges)
		go func() {
			err = http.ListenAndServe(listenAddr, router)
			if err != nil {
				log.Fatal(err)
			}
		}()

		log.Println("HTTP Webserver started successfully.")
		log.Println("Starting websocket handler.")
		StartWebsocketHandler()
	} else {
		log.Println("HTTP RESTful Webserver support disabled.")
	}

	<-bot.shutdown
	Shutdown()
}

// AdjustGoMaxProcs adjusts the maximum processes that the CPU can handle.
func AdjustGoMaxProcs() {
	log.Println("Adjusting bot runtime performance..")
	maxProcsEnv := os.Getenv("GOMAXPROCS")
	maxProcs := runtime.NumCPU()
	log.Println("Number of CPU's detected:", maxProcs)

	if maxProcsEnv != "" {
		log.Println("GOMAXPROCS env =", maxProcsEnv)
		env, err := strconv.Atoi(maxProcsEnv)
		if err != nil {
			log.Println("Unable to convert GOMAXPROCS to int, using", maxProcs)
		} else {
			maxProcs = env
		}
	}
	if i := runtime.GOMAXPROCS(maxProcs); i != maxProcs {
		log.Fatal("Go Max Procs were not set correctly.")
	}
	log.Println("Set GOMAXPROCS to:", maxProcs)
}

// HandleInterrupt monitors and captures the SIGTERM in a new goroutine then
// shuts down bot
func HandleInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-c
		log.Printf("Captured %v, shutdown requested.", sig)
		bot.shutdown <- true
	}()
}

// Shutdown correctly shuts down bot saving configuration files
func Shutdown() {
	log.Println("Bot shutting down..")

	if len(portfolio.Portfolio.Addresses) != 0 {
		bot.config.Portfolio = portfolio.Portfolio
	}

	if !bot.dryRun {
		err := bot.config.SaveConfig(bot.configFile)

		if err != nil {
			log.Println("Unable to save config.")
		} else {
			log.Println("Config file saved successfully.")
		}
	}

	log.Println("Exiting.")
	os.Exit(0)
}
