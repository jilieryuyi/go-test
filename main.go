package main

import (
	"flag"
	log "github.com/cihub/seelog"
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
	//dryRun     bool
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
	//log.SetFormatter(&log.TextFormatter{
	//	TimestampFormat: "2006-01-02 15:04:05",
	//	ForceColors:      true,
	//	QuoteEmptyFields: true,
	//	FullTimestamp:    true,
	//})
	//log.SetLevel(log.DebugLevel)
	// 初始化日志组件
	{
		logger, err := log.LoggerFromConfigAsFile(getWorkingPath()+"logger.xml")
		if err != nil {
			log.Errorf("init logger from %s error: %v", getWorkingPath()+"logger.xml", err)
		} else {
			log.ReplaceLogger(logger)
		}
	}
}

func main() {
	bot.shutdown = make(chan bool)
	HandleInterrupt()

	defaultPath, err := config.GetFilePath(getWorkingPath()+"config.json")
	if err != nil {
		log.Errorf("main config.GetFilePath fail, error=[%v]", err)
		return 
	}

	//Handle flags
	flag.StringVar(&bot.configFile, "config", defaultPath, "config file to load")
	flag.Parse()


	bot.config = &config.Cfg
	log.Infof("load config file %s", bot.configFile)

	err = bot.config.LoadConfig(bot.configFile)
	if err != nil {
		log.Errorf("bot.config.LoadConfig fail, error=[%s]", err)
		return 
	}

	AdjustGoMaxProcs()
	log.Infof("Bot '%s' started", bot.config.Name)
	//log.Infof("Bot dry run mode: %v", common.IsEnabled(bot.dryRun))

	log.Infof("Available Exchanges: %d. Enabled Exchanges: %d.",
		len(bot.config.Exchanges),
		bot.config.CountEnabledExchanges())

	common.HTTPClient = common.NewHTTPClientWithTimeout(bot.config.GlobalHTTPTimeout)
	log.Infof("Global HTTP request timeout: %v.", common.HTTPClient.Timeout)

	SetupExchanges()
	if len(bot.exchanges) == 0 {
		log.Errorf("No exchanges were able to be loaded. Exiting")
		return
	}

	log.Infof("Starting communication mediums..")
	bot.comms = communications.NewComm(bot.config.GetCommunicationsConfig())
	bot.comms.GetEnabledCommunicationMediums()

	log.Infof("Fiat display currency: %s.", bot.config.Currency.FiatDisplayCurrency)
	currency.BaseCurrency = bot.config.Currency.FiatDisplayCurrency
	currency.FXProviders = forexprovider.StartFXService(bot.config.GetCurrencyConfig().ForexProviders)
	log.Infof("Primary forex conversion provider: %s.", bot.config.GetPrimaryForexProvider())
	err = bot.config.RetrieveConfigCurrencyPairs(true)
	if err != nil {
		log.Errorf("Failed to retrieve config currency pairs. Error: %s", err)
		return 
	}
	err = currency.SeedCurrencyData(common.JoinStrings(currency.FiatCurrencies, ","))
	if err != nil {
		log.Errorf("Unable to fetch forex data. Error: %s", err)
		return 
	}

	bot.portfolio = &portfolio.Portfolio
	bot.portfolio.SeedPortfolio(bot.config.Portfolio)
	SeedExchangeAccountInfo(GetAllEnabledExchangeAccountInfo().Data)

	//go portfolio.StartPortfolioWatcher()
	//go TickerUpdaterRoutine()
	
	mongo := NewMongoDb("127.0.0.1:27017")
	defer mongo.Close()
	// 创建一个集合
	col := mongo.NewCollection("OrderBooks", "orderbooks")

	go OrderbookUpdaterRoutine(func(exchangeName, ticker string, lastUpdated time.Time,
		asks, bids []orderbook.Item,) {
		// 异步写入
		mongo.AsyncInsert(col, exchangeName, ticker, lastUpdated, asks, bids)
	})

	<-bot.shutdown
	Shutdown()
}

// AdjustGoMaxProcs adjusts the maximum processes that the CPU can handle.
func AdjustGoMaxProcs() {
	log.Infof("Adjusting bot runtime performance..")
	maxProcsEnv := os.Getenv("GOMAXPROCS")
	maxProcs := runtime.NumCPU()
	log.Infof("Number of CPU's detected:", maxProcs)

	if maxProcsEnv != "" {
		log.Infof("GOMAXPROCS env =", maxProcsEnv)
		env, err := strconv.Atoi(maxProcsEnv)
		if err != nil {
			log.Infof("Unable to convert GOMAXPROCS to int, using", maxProcs)
		} else {
			maxProcs = env
		}
	}
	if i := runtime.GOMAXPROCS(maxProcs); i != maxProcs {
		log.Errorf("Go Max Procs were not set correctly.")
		return
	}
	log.Infof("Set GOMAXPROCS to:", maxProcs)
}

// HandleInterrupt monitors and captures the SIGTERM in a new goroutine then
// shuts down bot
func HandleInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-c
		log.Infof("Captured %v, shutdown requested.", sig)
		bot.shutdown <- true
	}()
}

// Shutdown correctly shuts down bot saving configuration files
func Shutdown() {
	log.Infof("Bot shutting down..")

	if len(portfolio.Portfolio.Addresses) != 0 {
		bot.config.Portfolio = portfolio.Portfolio
	}

	log.Infof("Exiting.")
	os.Exit(0)
}
