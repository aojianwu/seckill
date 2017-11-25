package main

import (
	"flag"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lets-go-go/logger"
)

const (
	AreaBeijing      = "1_72_2799_0"
	AreaNanjingPukou = "12_904_50647_52660"
)

var (
	area   = flag.String("area", AreaNanjingPukou, "ship location string, default to Beijing")
	period = flag.Int("period", 500, "the refresh period when out of stock, unit: ms.")
	rush   = flag.Bool("rush", true, "continue to refresh when out of stock.")
	order  = flag.Bool("order", true, "submit the order to JingDong when get the Goods.")
	goods  = flag.String("goods", "4993751:1,4993773:1,4993737:1", `the goods you want to by, find it from JD website. 
	Single Goods:
	  2567304(:1)
	Multiple Goods:
	  2567304(:1),3133851(:2)`)
)

func main() {
	// subscribe to SIGINT signals
	stopChan := make(chan os.Signal)

	config := logger.DefalutConfig()
	config.LogFileName = "seckill"
	config.Level = logger.TRACE

	logger.Init(config)

	flag.Parse()

	gs := parseGoods(*goods)
	logger.Infof("[Area: %+v, Goods: %qv, Period: %+v, Rush: %+v, Order: %+v]",
		*area, gs, *period, *rush, *order)

	jd := NewJingDong(JDConfig{
		Period:     time.Millisecond * time.Duration(*period),
		ShipArea:   *area,
		AutoRush:   *rush,
		AutoSubmit: *order,
	})

	defer jd.Release()
	if err := jd.Login(); err == nil {
		jd.BatchBuy(gs)
	}

	<-stopChan // wait for SIGINT
}

// parseGoods parse the input goods list. Support to input multiple goods sperated
// by comma(,). With an (:count) after goods ID to specify the count of each goods.
//
// Example as following:
//
//   2567304				single goods with default count 1
//   2567304:3				single goods with count 3
//   2567304,3133851:4		multiple goods with defferent count 1, 4
//   2567304:2,3133851:5	...
//
func parseGoods(goods string) map[string]int {
	lst := make(map[string]int)
	if goods == "" {
		return lst
	}

	for _, good := range strings.Split(goods, ",") {
		pair := strings.Split(good, ":")
		name := strings.Trim(pair[0], " ")
		if len(pair) == 2 {
			v, _ := strconv.ParseInt(pair[1], 10, 32)
			lst[name] = int(v)
		} else {
			lst[name] = 1
		}
	}

	return lst
}
