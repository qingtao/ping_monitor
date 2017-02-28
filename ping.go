package main

import (
	"fmt"
	fastping "github.com/tatsushid/go-fastping"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

var localserver = "127.0.0.1"

//fastping received ICMP message
type response struct {
	addr *net.IPAddr
	rtt  time.Duration
}

//监控程序主体
type monitor struct {
	ping *fastping.Pinger
	//channel发送邮件
	mail    chan *Host
	logger  *log.Logger
	cfg     *Config
	results map[string]*response
}

//根据config和log创建monitor
func NewMonitor(cfg *Config, l *log.Logger) *monitor {
	var m = new(monitor)
	m.cfg = cfg
	m.logger = l

	//创建fastping.Pinger
	var p = fastping.NewPinger()

	//使用time包解析间隔时间，interval格式15s
	d, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		log.Fatalf("config interval %s\n", err)
	}
	if def_d := time.Second * time.Duration(mini_interval); d <= def_d {
		d = def_d
	}
	m.logger.Printf("Interval time: %+v\n", d)
	p.MaxRTT = d

	if cfg.Times <= int(mini_times) {
		cfg.Times = mini_times
	}
	m.logger.Printf("Max failed times: %+v\n", cfg.Times)
	m.logger.Println("-------------------------")

	hosts := cfg.Hosts

	m.results = make(map[string]*response)

	//添加需要监控的主机到fastping.Pinger
	for i := 0; i < len(hosts); i++ {
		ra, err := net.ResolveIPAddr("ip", hosts[i].Addr)
		if err != nil {
			m.logger.Printf("ResolveIPAddr: %s %s\n", hosts[i].Name, err)
			continue
		}
		m.logger.Printf("AddIPAddr: %s, [%s]\n", hosts[i].Name, ra)
		m.results[ra.String()] = nil
		p.AddIPAddr(ra)
	}
	m.logger.Println("-------------------------")

	//ICMP Echo大小32byte
	p.Size = 32
	m.ping = p

	m.mail = make(chan *Host, 2*len(hosts))

	return m
}

//发送报警邮件
func (m *monitor) resv() {
	resv := m.cfg.MailResv
	for h := range m.mail {
		if m.cfg.Debug {
			m.logger.Printf("[DEBUG] resv notice %s\n", h.Name)
		}
		read, ok := resv[h.AreaID]
		if ok {
			select {
			case read <- h:
				if m.cfg.Debug {
					m.logger.Printf("[DEBUG] resv send to read %s\n", h.Name)
				}
			case <-time.After(2 * time.Second):
				if m.cfg.Debug {
					m.logger.Printf("[DEBUG] resv send timeout %s\n", h.Name)
				}
				continue
			}
		}
	}
}

//检测监控主机自身状态，address是IP:PORT
func (m *monitor) heartbeat() error {
	_, err := net.LookupNS(m.cfg.Heartbeat)
	if err != nil {
		return err
	}
	return nil
}

//config中"debug"为true，则打印详细信息
func (m *monitor) debug(format string, v ...interface{}) {
	if m.cfg.Debug {
		m.logger.Printf(format, v...)
	}
}

func (m *monitor) status(ls string) {
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if tcp, err := net.ResolveTCPAddr("tcp", r.RemoteAddr); err == nil {
			if tcp.IP.Equal(net.ParseIP(localserver)) {
				var lock sync.Mutex
				lock.Lock()
				var hosts = make([]*Host, 0)
				var areaID = r.FormValue("q")
				if areaID != "" {
					for i := 0; i < len(m.cfg.Hosts); i++ {
						if m.cfg.Hosts[i].AreaID == areaID {
							hosts = append(hosts, m.cfg.Hosts[i])
						}
					}
				}
				var b []byte
				if len(hosts) == 0 {
					b, err = Json(m.cfg.Hosts)
				} else {
					b, err = Json(hosts)
				}
				lock.Unlock()
				if err != nil {
					m.logger.Println("json", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				callback := r.FormValue("callback")
				var s string
				if callback != "" {
					s = fmt.Sprintf("%s(%s)", callback, b)
				} else {
					s = string(b)
				}
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				fmt.Fprintf(w, "%s", s)
			}
		} else {
			m.debug("error reject access: %s\n", r.RemoteAddr)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "no allowed: %s", r.RemoteAddr)
		}
	})
	log.Fatal(http.ListenAndServe(ls, mux))
}

//启动监控
func (m *monitor) start() {
	onRecv, onIdle := make(chan *response), make(chan bool)
	//m.recv, m.idle = onRecv, onIdle
	m.ping.OnRecv = func(ra *net.IPAddr, rtt time.Duration) {
		onRecv <- &response{ra, rtt}
	}
	m.ping.OnIdle = func() {
		onIdle <- true
	}
	m.ping.RunLoop()

	times := m.cfg.Times

	for {
		select {
		case rm := <-onRecv:
			raddr := rm.addr.String()
			if _, ok := m.results[raddr]; ok {
				m.results[raddr] = rm
				host := Get(m.cfg.Hosts, raddr)

				//更新主机ping延迟时间
				host.RTT = rm.rtt.String()
				last := host.Last
				//更新主机最后ping正常时间
				host.Last = time.Now()
				//将失败时间设置为超时次数减一，如果当前计数大于等于times
				if host.Times >= times {
					host.Times = times - 1
					m.debug("[DEBUG] area: %s, %s failed: times %v, rtt %s\n", host.Area, host.Name, host.Times, host.RTT)
					//times计数减一
				} else if host.Times > 0 {
					host.Times -= 1
					m.debug("[DEBUG] area: %s, %s failed: times %v, rtt %s\n", host.Area, host.Name, host.Times, host.RTT)
				}
				//更新主机状态，如果times为零，并且主机状态为down
				if host.Times == 0 && !host.Stat {
					host.Stat = true
					//打印日志并发送邮件
					m.logger.Printf("[INFO] %s\n", host)

					//跳过后续操作，如果主机上次更新时间为0
					if last.IsZero() {
						m.debug("[DEBUG] %s ok:, last time: %v, rtt %s\n",
							host.Name, host.Last, host.RTT)
					} else {
						m.mail <- host
					}
				}
			}

		case <-onIdle:
			//测试监控服务器自身网络状态
			if err := m.heartbeat(); err != nil {
				m.logger.Printf("[ERROR] heartbeat to %s failed %s\n", m.cfg.Heartbeat, err)
				continue
			}

			for raddr, rm := range m.results {
				host := Get(m.cfg.Hosts, raddr)
				if rm == nil {
					//计数最大为15
					if host.Times < 15 {
						host.Times += 1
					} else {
						continue
					}
					m.debug("[DEBUG] area: %s, %s failed: times %v\n", host.Area, host.Name, host.Times)
					//更新主机状态，如果times大于config中指定的times，并且主机状态为up
					if host.Times >= times && host.Stat {
						host.Stat = false
						//打印日志，发送邮件
						m.logger.Printf("[EORROR] %s, failed times %d\n", host, host.Times)
						m.mail <- host
					}
				}
				m.results[raddr] = nil
			}
		}
	}
}
