package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/axgle/mahonia"
	sjson "github.com/bitly/go-simplejson"
	"github.com/lets-go-go/logger"
)

const (
	//URLSKUState    = "http://c0.3.cn/stock"
	URLSKUState    = "https://c0.3.cn/stocks"
	URLGoodsDets   = "http://item.jd.com/%s.html"
	URLGoodsPrice  = "http://p.3.cn/prices/mgets"
	URLAdd2Cart    = "https://cart.jd.com/gate.action"
	URLChangeCount = "http://cart.jd.com/changeNum.action"
	URLCartInfo    = "https://cart.jd.com/cart.action"
	URLOrderInfo   = "http://trade.jd.com/shopping/order/getOrderInfo.action"
	URLSubmitOrder = "http://trade.jd.com/shopping/order/submitOrder.action"
)

var (
	// URLForQR is the login related URL
	//
	URLForQR = [...]string{
		"https://passport.jd.com/new/login.aspx",
		"https://qr.m.jd.com/show",
		"https://qr.m.jd.com/check",
		"https://passport.jd.com/uc/qrCodeTicketValidation",
		"http://home.jd.com/getUserVerifyRight.action",
	}

	DefaultHeaders = map[string]string{
		// "User-Agent":      "Chrome/51.0.2704.103",
		// "ContentType":     "application/json", //"text/html; charset=utf-8",
		// "Connection":      "keep-alive",
		// "Accept-Encoding": "gzip, deflate",
		// "Accept-Language": "zh-CN,zh;q=0.8",

		"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/51.0.2704.103 Safari/537.36",
		"ContentType":     "text/html; charset=utf-8",
		"Accept-Encoding": "gzip, deflate, sdch",
		"Accept-Language": "zh-CN,zh;q=0.8",
		"Connection":      "keep-alive",
	}

	maxNameLen   = 40
	cookieFile   = "jd.cookies"
	qrCodeFile   = "jd.qr"
	strSeperater = strings.Repeat("+", 60)
)

// JDConfig ...
type JDConfig struct {
	Period     time.Duration // refresh period
	ShipArea   string        // shipping area
	AutoRush   bool          // continue rush when out of stock
	AutoSubmit bool          // whether submit the order
}

// SKUInfo ...
type SKUInfo struct {
	ID        string
	Price     string
	Count     int    // buying count
	State     string // stock state 33 : on sale, 34 : out of stock
	StateName string // "现货" / "无货"
	Name      string
	Link      string
	Attr      string
}

// JingDong wrap jing dong operation
type JingDong struct {
	JDConfig
	client *http.Client
	jar    *SimpleJar
	token  string
}

// NewJingDong create an object to wrap JingDong related operation
//
func NewJingDong(option JDConfig) *JingDong {
	jd := &JingDong{
		JDConfig: option,
	}

	jd.jar = NewSimpleJar(JarOption{
		JarType:  JarGob,
		Filename: cookieFile,
	})

	if err := jd.jar.Load(); err != nil {
		logger.Errorf("加载Cookies失败: %s", err)
		jd.jar.Clean()
	}

	jd.client = &http.Client{
		Timeout: time.Minute,
		Jar:     jd.jar,
	}

	return jd
}

// Release the resource opened
//
func (jd *JingDong) Release() {
	if jd.jar != nil {
		if err := jd.jar.Persist(); err != nil {
			logger.Errorf("Failed to persist cookiejar. error %+v.", err)
		}
	}
}

//
//
func truncate(str string) string {
	rs := []rune(str)
	if len(rs) > maxNameLen {
		return string(rs[:maxNameLen-1]) + "..."
	}
	return str
}

// if response data compressed by gzip, unzip first
//
func responseData(resp *http.Response) []byte {
	if resp == nil {
		return nil
	}

	var reader io.Reader
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		logger.Tracef("Encoding: %+v", resp.Header.Get("Content-Encoding"))
		reader, _ = gzip.NewReader(resp.Body)
	default:
		reader = resp.Body
	}

	data, err := ioutil.ReadAll(reader)
	if err != nil {
		logger.Errorf("读取响应数据失败: %+v", err)
		return nil
	}

	return data
}

//
//
func applyCustomHeader(req *http.Request, header map[string]string) {
	if req == nil || len(header) == 0 {
		return
	}

	for key, val := range header {
		req.Header.Set(key, val)
	}
}

//
func (jd *JingDong) validateLogin(URL string) bool {
	var (
		err  error
		req  *http.Request
		resp *http.Response
	)

	if req, err = http.NewRequest("GET", URL, nil); err != nil {
		logger.Errorf("请求（%+v）失败: %+v", URL, err)
		return false
	}

	jd.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// disable redirect
		return http.ErrUseLastResponse
	}

	defer func() {
		// restore to default
		jd.client.CheckRedirect = nil
	}()

	if resp, err = jd.client.Do(req); err != nil {
		logger.Warnf("需要重新登录: %+v", err)
		return false
	}

	defer resp.Body.Close()
	data, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		logger.Warnf("需要重新登录")
		return false
	}

	logger.Tracef("Response Data: %s", string(data))
	return true
}

// load the login page
//
func (jd *JingDong) loginPage(URL string) error {
	var (
		err  error
		req  *http.Request
		resp *http.Response
	)

	if req, err = http.NewRequest("GET", URL, nil); err != nil {
		logger.Warnf("请求（%+v）失败: %+v", URL, err)
		return err
	}

	applyCustomHeader(req, DefaultHeaders)

	if resp, err = jd.client.Do(req); err != nil {
		logger.Warnf("请求登录页失败: %+v", err)
		return err
	}

	defer resp.Body.Close()
	return nil
}

// download the QR Code
//
func (jd *JingDong) loadQRCode(URL string) (string, error) {
	var (
		err  error
		req  *http.Request
		resp *http.Response
	)

	u, _ := url.Parse(URL)
	q := u.Query()
	q.Set("appid", strconv.Itoa(133))
	q.Set("size", strconv.Itoa(147))
	q.Set("t", strconv.FormatInt(time.Now().Unix()*1000, 10))
	u.RawQuery = q.Encode()

	if req, err = http.NewRequest("GET", u.String(), nil); err != nil {
		logger.Errorf("请求（%+v）失败: %+v", URL, err)
		return "", err
	}

	applyCustomHeader(req, DefaultHeaders)
	if resp, err = jd.client.Do(req); err != nil {
		logger.Errorf("下载二维码失败: %+v", err)
		return "", err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Errorf("http status : %d/%s", resp.StatusCode, resp.Status)
	}

	// from mime get QRCode image type
	//  content-type:image/png
	//
	filename := qrCodeFile + ".png"
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if typ, e := mime.ExtensionsByType(mt); e == nil {
		filename = qrCodeFile + typ[0]
	}

	dir, _ := os.Getwd()
	filename = filepath.Join(dir, filename)
	logger.Tracef("QR Image: %s", filename)

	file, _ := os.Create(filename)
	defer file.Close()

	if _, err = io.Copy(file, resp.Body); err != nil {
		logger.Errorf("下载二维码失败: %+v", err)
		return "", err
	}

	return filename, nil
}

// wait scan result
//
func (jd *JingDong) waitForScan(URL string) error {
	var (
		err    error
		req    *http.Request
		resp   *http.Response
		wlfstk string
	)

	for _, c := range jd.jar.Cookies(nil) {
		if c.Name == "wlfstk_smdl" {
			wlfstk = c.Value
			break
		}
	}

	u, _ := url.Parse(URL)
	q := u.Query()
	q.Set("callback", "jQuery123456")
	q.Set("appid", strconv.Itoa(133))
	q.Set("token", wlfstk)
	q.Set("_", strconv.FormatInt(time.Now().Unix()*1000, 10))
	u.RawQuery = q.Encode()

	if req, err = http.NewRequest("GET", u.String(), nil); err != nil {
		logger.Infof("请求（%+v）失败: %+v", URL, err)
		return err
	}

	// mush have
	req.Host = "qr.m.jd.com"
	req.Header.Set("Referer", "https://passport.jd.com/new/login.aspx")
	applyCustomHeader(req, DefaultHeaders)

	for retry := 50; retry != 0; retry-- {
		if resp, err = jd.client.Do(req); err != nil {
			logger.Warnf("二维码失效：%+v", err)
			break
		}

		if resp.StatusCode == http.StatusOK {
			respMsg := string(responseData(resp))
			resp.Body.Close()

			n1 := strings.Index(respMsg, "(")
			n2 := strings.Index(respMsg, ")")

			var js *sjson.Json
			if js, err = sjson.NewJson([]byte(respMsg[n1+1 : n2])); err != nil {
				logger.Errorf("解析响应数据失败: %+v", err)
				logger.Tracef("Response data  : %+v", respMsg)
				logger.Tracef("Response Header: %+v", resp.Header)
				break
			}

			code := js.Get("code").MustInt()
			if code == 200 {
				jd.token = js.Get("ticket").MustString()
				logger.Infof("token : %+v", jd.token)
				break
			} else {
				logger.Infof("%+v : %s", code, js.Get("msg").MustString())
				time.Sleep(time.Second * 3)
			}
		} else {
			resp.Body.Close()
		}
	}

	if jd.token == "" {
		err = fmt.Errorf("未检测到QR扫码结果")
		return err
	}

	return nil
}

// validate QR token
//
func (jd *JingDong) validateQRToken(URL string) error {
	var (
		err  error
		req  *http.Request
		resp *http.Response
	)

	u, _ := url.Parse(URL)
	q := u.Query()
	q.Set("t", jd.token)
	u.RawQuery = q.Encode()

	if req, err = http.NewRequest("GET", u.String(), nil); err != nil {
		logger.Warnf("请求（%+v）失败: %+v", URL, err)
		return err
	}
	if resp, err = jd.client.Do(req); err != nil {
		logger.Errorf("二维码登陆校验失败: %+v", err)
		return nil
	}

	//
	// 京东有时候会认为当前登录有危险，需要手动验证
	// url: https://safe.jd.com/dangerousVerify/index.action?username=...
	//
	if resp.Header.Get("P3P") == "" {
		var res struct {
			ReturnCode int    `json:"returnCode"`
			Token      string `json:"token"`
			URL        string `json:"url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
			if res.URL != "" {
				verifyURL := res.URL
				if !strings.HasPrefix(verifyURL, "https:") {
					verifyURL = "https:" + verifyURL
				}
				logger.Errorf("安全验证: %s", verifyURL)
				jd.runCommand(verifyURL)
			}
		}
		return fmt.Errorf("login failed")
	}

	if resp.StatusCode == http.StatusOK {
		//data, _ := ioutil.ReadAll(resp.Body)
		//logger.Infof("Body: %s.", string(data))
		logger.Infof("登陆成功, P3P: %s", resp.Header.Get("P3P"))
	} else {
		logger.Warnf("登陆失败")
		err = fmt.Errorf("%+v", resp.Status)
	}

	resp.Body.Close()
	return nil
}

func (jd *JingDong) runCommand(strCmd string) error {
	var err error
	var cmd *exec.Cmd

	// for different platform
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", strCmd)
	case "linux":
		cmd = exec.Command("eog", strCmd)
	default:
		cmd = exec.Command("open", strCmd)
	}

	// just start, do not wait it complete
	if err = cmd.Start(); err != nil {
		if runtime.GOOS == "linux" {
			cmd = exec.Command("gnome-open", strCmd)
			return cmd.Start()
		}
		return err
	}
	return nil
}

// Login used to login JD by QR code.
// if the cookies file exits, will try cookies first.
//
func (jd *JingDong) Login(args ...interface{}) error {
	logger.Infof(strSeperater)

	if jd.validateLogin(URLForQR[4]) {
		logger.Infof("无需重新登录")
		return nil
	}

	var (
		err   error
		qrImg string
	)

	logger.Infof("请打开京东手机客户端，准备扫码登陆:")
	jd.jar.Clean()

	if err = jd.loginPage(URLForQR[0]); err != nil {
		return err
	}

	if qrImg, err = jd.loadQRCode(URLForQR[1]); err != nil {
		return err
	}

	// just start, do not wait it complete
	if err = jd.runCommand(qrImg); err != nil {
		logger.Warnf("打开二维码图片失败: %+v.", err)
		return err
	}

	if err = jd.waitForScan(URLForQR[2]); err != nil {
		return err
	}

	if err = jd.validateQRToken(URLForQR[3]); err != nil {
		return err
	}

	//http.Post()
	return nil
}

// CartDetails get the shopping cart details
//
func (jd *JingDong) CartDetails() error {
	logger.Infof(strSeperater)
	logger.Infof("购物车详情>")

	var (
		err  error
		req  *http.Request
		resp *http.Response
		doc  *goquery.Document
	)

	if req, err = http.NewRequest("GET", URLCartInfo, nil); err != nil {
		logger.Errorf("请求（%+v）失败: %+v", URLCartInfo, err)
		return err
	}

	if resp, err = jd.client.Do(req); err != nil {
		logger.Errorf("获取购物车详情错误: %+v", err)
		return err
	}

	defer resp.Body.Close()
	if doc, err = goquery.NewDocumentFromReader(resp.Body); err != nil {
		logger.Errorf("分析购物车页面错误: %+v.", err)
		return err
	}

	logger.Infof("购买  数量  价格      总价      编号        商品")
	cartFormat := "%-6s%-6s%-10s%-10s%-12s%s"

	doc.Find("div.item-form").Each(func(i int, p *goquery.Selection) {
		check := " -"
		checkTag := p.Find("div.cart-checkbox input").Eq(0)
		if _, exist := checkTag.Attr("checked"); exist {
			check = " +"
		}

		count := "0"
		countTag := p.Find("div.quantity-form input").Eq(0)
		if val, exist := countTag.Attr("value"); exist {
			count = val
		}

		pid := ""
		hrefTag := p.Find("div.p-img a").Eq(0)
		if href, exist := hrefTag.Attr("href"); exist {
			// http://item.jd.com/2967929.html
			pos1 := strings.LastIndex(href, "/")
			pos2 := strings.LastIndex(href, ".")
			pid = href[pos1+1 : pos2]
		}

		price := strings.Trim(p.Find("div.p-price strong").Eq(0).Text(), " ")
		total := strings.Trim(p.Find("div.p-sum strong").Eq(0).Text(), " ")
		gname := strings.Trim(p.Find("div.p-name a").Eq(0).Text(), " \n\t")
		gname = truncate(gname)
		logger.Infof(cartFormat, check, count, price, total, pid, gname)
	})

	totalCount := strings.Trim(doc.Find("div.amount-sum em").Eq(0).Text(), " ")
	totalValue := strings.Trim(doc.Find("span.sumPrice em").Eq(0).Text(), " ")
	logger.Infof("总数: %s", totalCount)
	logger.Infof("总额: %s", totalValue)

	return nil
}

// OrderInfo shows the order detail information
//
func (jd *JingDong) OrderInfo() error {
	var (
		err  error
		req  *http.Request
		resp *http.Response
		doc  *goquery.Document
	)

	logger.Infof(strSeperater)
	logger.Infof("订单详情>")

	u, _ := url.Parse(URLOrderInfo)
	q := u.Query()
	q.Set("rid", strconv.FormatInt(time.Now().Unix()*1000, 10))
	u.RawQuery = q.Encode()

	if req, err = http.NewRequest("GET", u.String(), nil); err != nil {
		logger.Errorf("请求（%+v）失败: %+v", URLCartInfo, err)
		return err
	}

	if resp, err = jd.client.Do(req); err != nil {
		logger.Errorf("获取订单页错误: %+v", err)
		return err
	}

	defer resp.Body.Close()
	if doc, err = goquery.NewDocumentFromReader(resp.Body); err != nil {
		logger.Errorf("分析订单页错误: %+v.", err)
		return err
	}

	//h, _ := doc.Find("div.order-summary").Html()
	//logger.Tracef("订单页：%s", h)

	if order := doc.Find("div.order-summary").Eq(0); order != nil {
		warePrice := strings.Trim(order.Find("#warePriceId").Text(), " \t\n")
		shipPrice := strings.Trim(order.Find("#freightPriceId").Text(), " \t\n")
		logger.Infof("总金额: %s", warePrice)
		logger.Infof("　运费: %s", shipPrice)

	}

	if sum := doc.Find("div.trade-foot").Eq(0); sum != nil {
		payment := strings.Trim(sum.Find("#sumPayPriceId").Text(), " \t\n")
		phone := strings.Trim(sum.Find("#sendMobile").Text(), " \t\n")
		addr := strings.Trim(sum.Find("#sendAddr").Text(), " \t\n")
		logger.Infof("应付款: %s", payment)
		logger.Infof("%s", phone)
		logger.Infof("%s", addr)
	}

	return nil
}

// SubmitOrder ... submit order to JingDong, return orderID or error
//
func (jd *JingDong) SubmitOrder() (string, error) {
	if !jd.AutoSubmit {
		return "", nil
	}

	logger.Infof(strSeperater)
	logger.Infof("提交订单>")

	data, err := jd.getResponse("POST", URLSubmitOrder, func(URL string) string {
		queryString := map[string]string{
			"overseaPurchaseCookies":             "",
			"submitOrderParam.fp":                "",
			"submitOrderParam.eid":               "",
			"submitOrderParam.btSupport":         "1",
			"submitOrderParam.sopNotPutInvoice":  "false",
			"submitOrderParam.ignorePriceChange": "0",
			"submitOrderParam.trackID":           jd.jar.Get("TrackID"),
		}
		u, _ := url.Parse(URLSubmitOrder)
		q := u.Query()
		for k, v := range queryString {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		return u.String()
	})

	if err != nil {
		logger.Errorf("提交订单失败: %+v", err)
		return "", err
	}

	var js *sjson.Json
	if js, err = sjson.NewJson(data); err != nil {
		logger.Tracef("Reponse Data: %s", data)
		logger.Errorf("无法解析订单响应数据: %+v", err)
		return "", err
	}

	logger.Tracef("订单: %s", data)

	if succ, _ := js.Get("success").Bool(); succ {
		orderID, _ := js.Get("orderId").Int64()
		logger.Infof("下单成功，订单号：%d", orderID)
		return fmt.Sprintf("%d", orderID), nil
	}

	res, _ := js.Get("resultCode").String()
	msg, _ := js.Get("message").String()
	logger.Errorf("下单失败, %s : %s", res, msg)
	return "", fmt.Errorf("failed to submit order (%s : %s)", res, msg)
}

// wrap http get/post request
//
func (jd *JingDong) getResponse(method, URL string, queryFun func(URL string) string) ([]byte, error) {
	var (
		err  error
		req  *http.Request
		resp *http.Response
	)

	queryURL := URL
	if queryFun != nil {
		queryURL = queryFun(URL)
	}

	if req, err = http.NewRequest(method, queryURL, nil); err != nil {
		return nil, err
	}
	applyCustomHeader(req, DefaultHeaders)

	if resp, err = jd.client.Do(req); err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	var reader io.Reader

	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, _ = gzip.NewReader(resp.Body)
	default:
		reader = resp.Body
	}

	return ioutil.ReadAll(reader)
}

// getPrice return sku price by ID
//
//  [{"id":"J_5105046","p":"1999.00","m":"9999.00","op":"1999.00","tpp":"1949.00"}]
//
func (jd *JingDong) getPrice(ID string) (string, error) {
	data, err := jd.getResponse("GET", URLGoodsPrice, func(URL string) string {
		u, _ := url.Parse(URLGoodsPrice)
		q := u.Query()
		q.Set("type", "1")
		q.Set("skuIds", "J_"+ID)
		q.Set("pduid", strconv.FormatInt(time.Now().Unix()*1000, 10))
		u.RawQuery = q.Encode()
		return u.String()
	})

	if err != nil {
		logger.Errorf("获取商品（%s）价格失败: %+v", ID, err)
		return "", err
	}

	var js *sjson.Json
	if js, err = sjson.NewJson(data); err != nil {
		logger.Tracef("Response Data: %s", data)
		logger.Errorf("解析响应数据失败: %+v", err)
		return "", err
	}

	return js.GetIndex(0).Get("p").String()
}

// stockState return stock state
// http://c0.3.cn/stock?skuId=531065&area=1_72_2799_0&cat=1,1,1&buyNum=1
// http://c0.3.cn/stock?skuId=531065&area=1_72_2799_0&cat=1,1,1
// https://c0.3.cn/stocks?type=getstocks&skuIds=4099139&area=1_72_2799_0&_=1499755881870
//
// {"3133811":{"StockState":33,"freshEdi":null,"skuState":1,"PopType":0,"sidDely":"40",
//	"channel":1,"StockStateName":"现货","rid":null,"rfg":0,"ArrivalDate":"",
//  "IsPurchase":true,"rn":-1}}
func (jd *JingDong) stockState(ID string) (string, string, error) {
	data, err := jd.getResponse("GET", URLSKUState, func(URL string) string {
		u, _ := url.Parse(URL)
		q := u.Query()
		q.Set("type", "getstocks")
		q.Set("skuIds", ID)
		q.Set("area", jd.ShipArea)
		q.Set("_", strconv.FormatInt(time.Now().Unix()*1000, 10))
		//q.Set("cat", "1,1,1")
		//q.Set("buyNum", strconv.Itoa(1))
		u.RawQuery = q.Encode()
		return u.String()
	})

	if err != nil {
		logger.Errorf("获取商品（%s）库存失败: %+v", ID, err)
		return "", "", err
	}

	// return GBK encoding
	dec := mahonia.NewDecoder("gbk")
	decString := dec.ConvertString(string(data))
	//logger.Tracef(decString)

	var js *sjson.Json
	if js, err = sjson.NewJson([]byte(decString)); err != nil {
		logger.Tracef("Response Data: %s", data)
		logger.Errorf("解析库存数据失败: %+v", err)
		return "", "", err
	}

	//if sku, exist := js.CheckGet("stock"); exist {
	if sku, exist := js.CheckGet(ID); exist {
		skuState, _ := sku.Get("StockState").Int()
		skuStateName, _ := sku.Get("StockStateName").String()
		return strconv.Itoa(skuState), skuStateName, nil
	}

	return "", "", fmt.Errorf("无效响应数据")
}

// skuDetail get sku detail information
//
func (jd *JingDong) skuDetail(ID string, count int) (*SKUInfo, error) {
	sku := &SKUInfo{ID: ID, Count: count}

	// response context encoding by GBK
	//
	itemURL := fmt.Sprintf("http://item.jd.com/%s.html", ID)

	logger.Tracef("商品页面链接: %+v", itemURL)
	data, err := jd.getResponse("GET", itemURL, nil)
	if err != nil {
		logger.Errorf("获取商品页面失败: %+v", err)
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewBuffer(data))
	if err != nil {
		logger.Errorf("解析商品页面失败: %+v", err)
		return nil, err
	}

	if link, exist := doc.Find("a#InitCartUrl").Attr("href"); exist {
		sku.Link = link
		if !strings.HasPrefix(link, "https:") {
			sku.Link = "https:" + link
		}
	}

	dec := mahonia.NewDecoder("gbk")
	//rd := dec.NewReader()

	sku.Name = strings.Trim(dec.ConvertString(doc.Find("div.sku-name").Text()), " \t\n")
	// g.Name = truncate(g.Name)

	sku.Price, _ = jd.getPrice(ID)
	sku.State, sku.StateName, _ = jd.stockState(ID)

	if sku.Link == "" || sku.Count != 1 {
		u, _ := url.Parse(URLAdd2Cart)
		q := u.Query()
		q.Set("pid", sku.ID)
		q.Set("pcount", strconv.Itoa(sku.Count))
		q.Set("ptype", "1")
		u.RawQuery = q.Encode()
		sku.Link = u.String()
	}
	logger.Infof("购买链接: %s", sku.Link)

	if _, err := url.Parse(sku.Link); err != nil {
		logger.Errorf("商品购买链接无效: <%s>", sku.Link)
		// return fmt.Errorf("无效商品购买链接<%s>", sku.Link)
	}

	logger.Infof(strSeperater)
	logger.Infof("商品详情>")
	logger.Infof("编号: %s, 品名: %s, 库存: %s, 价格: %s, URL:%s", sku.ID, sku.Name, sku.StateName, sku.Price, sku.Link)

	return sku, nil
}

func (jd *JingDong) changeCount(ID string, count int) (int, error) {
	data, err := jd.getResponse("POST", URLChangeCount, func(URL string) string {
		u, _ := url.Parse(URL)
		q := u.Query()
		q.Set("venderId", "8888")
		q.Set("targetId", "0")
		q.Set("promoID", "0")
		q.Set("outSkus", "")
		q.Set("ptype", "1")
		q.Set("pid", ID)
		q.Set("pcount", strconv.Itoa(count))
		q.Set("random", strconv.FormatFloat(rand.Float64(), 'f', 16, 64))
		q.Set("locationId", jd.ShipArea)
		u.RawQuery = q.Encode()
		return u.String()
	})

	if err != nil {
		logger.Errorf("修改商品数量失败: %+v", err)
		return 0, err
	}

	js, _ := sjson.NewJson(data)
	return js.Get("pcount").Int()
}

func (jd *JingDong) addToCart(sku *SKUInfo) (bool, error) {
	var (
		err  error
		data []byte
		doc  *goquery.Document
	)

	if data, err = jd.getResponse("GET", sku.Link, nil); err != nil {
		logger.Errorf("商品(%s)购买失败: %+v", sku.ID, err)
		return false, err
	}

	if doc, err = goquery.NewDocumentFromReader(bytes.NewBuffer(data)); err != nil {
		logger.Errorf("响应解析失败: %+v", err)
		return false, err
	}

	succFlag := doc.Find("h3.ftx-02").Text()
	//fmt.Println(succFlag)

	if succFlag == "" {
		succFlag = doc.Find("div.p-name a").Text()
	}

	if succFlag != "" {
		count := 0
		if sku.Count > 1 {
			count, err = jd.changeCount(sku.ID, sku.Count)
		}

		if count > 0 {
			logger.Infof("购买结果：成功加入进购物车 [%d] 个 [%s]", count, sku.Name)
			return false, nil
		}
	}

	return false, err
}

func (jd *JingDong) buyGoods(sku *SKUInfo) error {
	ret, err := jd.addToCart(sku)

	if ret {
		jd.CartDetails()
		jd.OrderInfo()

		jd.SubmitOrder()
	} else {
		if err != nil {
			logger.Errorf("add to cart error:%v", err)
		}
	}

	return nil
}

func (jd *JingDong) checkGoods(sku *SKUInfo) (ret bool, err error) {

	if jd.AutoRush {
		checkTimer := time.NewTimer(jd.Period)

		for {
			select {
			case <-checkTimer.C:
				sku.State, sku.StateName, err = jd.stockState(sku.ID)
				if err != nil {
					logger.Errorf("获取(%s)库存失败: %+v", sku.ID, err)
				}
				if sku.State == "33" {
					ret = true
					break
				}
				logger.Warnf("%s : %v, %s", sku.StateName, sku.ID, sku.Name)
				checkTimer.Reset(jd.Period)
			}

		}

	} else {
		sku.State, sku.StateName, err = jd.stockState(sku.ID)
	}

	return ret, err
}

func (jd *JingDong) Buy(id string, count int) {
	// for {
	sku, err := jd.skuDetail(id, count)
	if err == nil {
		sku.Count = count
		ret, err := jd.checkGoods(sku)
		if ret {
			jd.buyGoods(sku)

		} else {
			if err != nil {
				logger.Errorf("check goods failed.err:%+v", err)
			}
		}

		// break
	} else {
		logger.Errorf("get sku detail error:%v", err)
	}
	// }

}

func (jd *JingDong) BatchBuy(skuLst map[string]int) {
	logger.Tracef("BatchBuy %+v.....", skuLst)
	for id, cnt := range skuLst {
		logger.Tracef("准备 %v (%v).....", id, cnt)
		go func(id string, cnt int) {
			jd.Buy(id, cnt)
		}(id, cnt)
	}

	logger.Tracef("BatchBuy end")

}
