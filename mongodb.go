package main

import (
	"os"
	"gopkg.in/mgo.v2"
	"time"
	"github.com/thrasher-/gocryptotrader/exchanges/orderbook"
	log "github.com/sirupsen/logrus"
)

type Mongodb struct {
	session *mgo.Session
	writeChan chan *asyncWriteData
}

type asyncWriteData struct {
	col *mgo.Collection
	data map[string]interface{}
}

func NewMongoDb(address string) *Mongodb {
	session, err := mgo.Dial(address)
	if err != nil {
		log.Panicf("mgo.Dial fail, error=[%v]", err)
		os.Exit(1)
	}
	//session.Login()
	session.SetSafe(&mgo.Safe{})
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
	m.writeChan <- &asyncWriteData{
		col: col,
		data: map[string]interface{}{
			"ExchangeName": exchangeName,
			"Ticker":	    ticker,
			"Timestamp":	lastUpdated,
			"Asks":		    asks,
			"Bids":		    bids,
		},
	}
}
