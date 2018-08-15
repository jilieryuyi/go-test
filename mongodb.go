package main

import (
	"fmt"
	"os"
	"gopkg.in/mgo.v2"
	"time"
	"github.com/thrasher-/gocryptotrader/exchanges/orderbook"
)

type Mongodb struct {
	session *mgo.Session
	writeChan chan *asyncWriteData//map[string]interface{}
}

type asyncWriteData struct {
	col *mgo.Collection
	data map[string]interface{}
}

func NewMongoDb(address string) *Mongodb {
	session, err := mgo.Dial(address)//"127.0.0.1:27017")

	if err != nil {
		fmt.Printf("Can't connect to mongo, go error %v\n", err)
		os.Exit(1)
	}

	//session.Login()

	//defer session.Close()

	// SetSafe changes the session safety mode.
	// If the safe parameter is nil, the session is put in unsafe mode, and writes become fire-and-forget,
	// without error checking. The unsafe mode is faster since operations won't hold on waiting for a confirmation.
	// http://godoc.org/labix.org/v2/mgo#Session.SetMode.
	session.SetSafe(&mgo.Safe{})

	// get collection
	//collection := session.DB("OrderBooks").C("orderbooks")
	m := &Mongodb{
		session: session,
		writeChan: make(chan *asyncWriteData, 1024),
	}
	return m
}

func (m *Mongodb) Close() {
	m.session.Close()
	close(m.writeChan)
}

func (m *Mongodb) NewCollection(dbName, colName string) *mgo.Collection {
	return m.session.DB(dbName).C(colName)
}

func (m *Mongodb) process() {
	//col := m.NewCollection("OrderBooks", "orderbooks")
	for {
		select {
		case col, ok := <- m.writeChan:
			if !ok {
				return
			}
			err := col.col.Insert(col.data)
			// 如果失败，等待1秒后重试，最多重试3次
			if err != nil {
				for i := 0; i < 3; i++ {
					time.Sleep(time.Second)
					err = col.col.Insert(col.data)
					if err == nil {
						break
					}
				}
			}
		}
	}
}

func (m *Mongodb) AsyncInsert(
	col *mgo.Collection, exchangeName,
	ticker string, lastUpdated time.Time,
	asks, bids []orderbook.Item,
) {
	//col.Insert(map[string]interface{}{
	//	"ExchangeName": exchangeName,
	//	"Ticker":	    ticker,//exchange.FormatCurrency(p).String(),
	//	"Timestamp":	lastUpdated,//result.LastUpdated,
	//	"Asks":		asks,//result.Asks,
	//	"Bids":		bids,//result.Bids,
	//})
	m.writeChan <- &asyncWriteData{
		col: col,
		data: map[string]interface{}{
			"ExchangeName": exchangeName,
			"Ticker":	    ticker,//exchange.FormatCurrency(p).String(),
			"Timestamp":	lastUpdated,//result.LastUpdated,
			"Asks":		asks,//result.Asks,
			"Bids":		bids,//result.Bids,
		},
	}
}
