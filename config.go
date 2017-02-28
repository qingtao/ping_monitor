package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

//主机信息
type Host struct {
	Name   string    `json:"name"`
	Addr   string    `json:"address"`
	RTT    string    `json:"rtt,omitempty"`
	Stat   bool      `json:"status"`
	Times  int       `json:"failed,omitempty"`
	Last   time.Time `json:"last,omitempty"`
	AreaID string    `json:"areaID"`
	Area   string    `json:"area"`
}

func Get(h []*Host, addr string) *Host {
	for i := 0; i < len(h); i++ {
		if h[i].Addr == addr {
			return h[i]
		}
	}
	return nil
}

//实现String()，返回字符串
func (h *Host) String() string {
	s := "down"
	if h.Stat {
		s = "up"
	}
	var format = "2006-01-02 15:04:05 CST"
	str := fmt.Sprintf("area: %s name: %s address: %s %s, last time: %s",
		h.Area, h.Name, h.Addr, s, h.Last.Format(format))
	return str
}

//获取程序所在目录
func BaseDir() string {
	dir, err := filepath.Abs(os.Args[0])
	if err != nil {
		log.Fatalln(err)
	}
	return filepath.Dir(dir)
}

//邮件信息
type Mailer struct {
	RcptTo   string                `json:"rcpt_to"`
	MailFrom string                `json:"mail_from"`
	Secret   string                `json:"secret"`
	SmtpHost string                `json:"smtp_server"`
	SmtpPort string                `json:"smtp_port"`
	Emails   map[string]string     `json:"-"`
	Mailresv map[string]chan *Host `json:"-"`
}

//配置数据结构
type Config struct {
	Debug     bool                  `json:"debug"`
	Mail      Mailer                `json:"mail"`
	RelayTime int                   `json:"relay_time,omitempty"`
	Heartbeat string                `json:"heartbeat"`
	Interval  string                `json:"interval"`
	Times     int                   `json:"times,string"`
	Hosts     []*Host               `json:"hosts"`
	MailResv  map[string]chan *Host `json:"-"`
}

//读取配置信息
func ConfigReader(jc *jsonconfig) *Config {
	var c Config
	c.Debug = jc.Global.Debug
	c.Mail = jc.Global.Mail
	c.Heartbeat = jc.Global.Heartbeat
	c.Interval = jc.Global.Interval
	c.Times = jc.Global.Times
	c.MailResv = make(map[string]chan *Host)
	c.RelayTime = jc.Global.RelayTime
	var emails = make(map[string]string)
	for k, group := range jc.Groups {
		emails[k] = group.Email
		for i := 0; i < len(group.Hosts); i++ {
			h := group.Hosts[i]
			mrsv := make(chan *Host, len(group.Hosts))
			c.MailResv[group.Area] = mrsv
			c.Hosts = append(c.Hosts, &Host{
				Name:   h.Name,
				Addr:   h.Addr,
				Area:   group.Name,
				AreaID: group.Area,
			})
		}
		c.Mail.Emails = emails
	}
	return &c
}

func Mkdir(dir string) {
	if _, err := os.Stat(dir); err != nil {
		err = os.MkdirAll(dir, os.ModeDir|0755)
		if err != nil {
			log.Fatalln(err)
		}
	}
}

//创建日志文件
func Log(f string) *log.Logger {
	//日志保存目录
	file, err := os.Create(f)
	if err != nil {
		log.Fatalln(err)
	}
	return log.New(file, "", 1|2)
}
