package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const configDir = "conf.d"

var re = regexp.MustCompile(`[^[:digit:]]`)

type jsonhost struct {
	Name string `json:"name"`
	Addr string `json:"address"`
}

//按组分类的主机信息
type Group struct {
	Area  string      `json:"area"`
	Name  string      `json:"name"`
	Email string      `json:"email"`
	Hosts []*jsonhost `json:"hosts"`

	//配置保存路径: 不打印JSON
	path string
}

//全局配置
type Global struct {
	Debug     bool   `json:"debug"`
	Heartbeat string `json:"heartbeat"`
	Interval  string `json:"interval"`
	Times     int    `json:"times,string"`
	RelayTime int    `json:"relay_time"`
	Mail      Mailer `json:"mail"`
}

func ReadGroup(file string) (*Group, error) {
	bs, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var g Group
	err = json.Unmarshal(bs, &g)
	if err != nil {
		return nil, err
	}
	g.path = file
	return &g, nil
}

type jsonconfig struct {
	Global *Global           `json:"global"`
	Groups map[string]*Group `json:"groups,omitempty"`
}

//TO /status
func Json(hs []*Host) ([]byte, error) {
	b, err := json.Marshal(hs)
	if err != nil {
		return nil, err
	}
	return b, nil
}

//读取json格式的配置文件
func JsonConfigReader(cpath, gdir string) (*jsonconfig, error) {
	bs, err := ioutil.ReadFile(cpath)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("%s, %s", err, cpath))
	}

	//fmt.Printf("%s\n", bs)
	var global Global
	err = json.Unmarshal(bs, &global)
	if err != nil {
		return nil, err
	}

	files, err := filepath.Glob(filepath.Join(gdir, `*.json`))
	if err != nil {
		return nil, err
	}

	groups := make(map[string]*Group)
	for _, f := range files {
		g, err := ReadGroup(f)
		if err != nil {
			return nil, err
		}
		//println(g.Area)
		groups[g.Area] = g
	}

	return &jsonconfig{Global: &global, Groups: groups}, nil
}

//写入配置文件: back为true时备份当前配置
func JsonConfigWrite(file string, v interface{}, back bool) error {
	if back {
		backup := file + ".back"
		if err := os.Rename(file, backup); err != nil {
			return err
		}
	}
	if err := createConfig(file, v); err != nil {
		return err
	}
	return nil
}

//保存全部配置: 包括全局和分组
func (jc *jsonconfig) Save(dir string, back bool) error {
	for area, group := range jc.Groups {
		gpath := filepath.Join(dir, area+".json")
		if err := JsonConfigWrite(gpath, group, back); err != nil {
			return err
		}
	}

	cpath := filepath.Join(dir, "config.json")
	if err := JsonConfigWrite(cpath, jc.Global, back); err != nil {
		return err
	}
	return nil
}

//写入json到文件
func createConfig(file string, c interface{}) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err = ioutil.WriteFile(file, b, 0666); err != nil {
		fmt.Println("generate configure", err)
		return err
	}
	return nil
}

//command 用于启动或者关闭ping服务进程
type command struct {
	*os.Process
	localPort string
	logger    *log.Logger
}

//add whitelist to http.ServerMux
type Server struct {
	*http.ServeMux
	IPS    []net.IP
	IPNET  []*net.IPNet
	Logger *log.Logger
	cmd    *command
	exit   chan int
}

//读取白名单: 每一行一个IP或者网段
func NewServer(whitelist string, l *log.Logger) (*Server, error) {
	mux := http.NewServeMux()
	str, err := ioutil.ReadFile(whitelist)
	if err != nil {
		return nil, err
	}

	var ips []net.IP
	var ipnet []*net.IPNet

	for _, allow := range strings.Fields(string(str)) {
		_, ipNet, err := net.ParseCIDR(allow)
		if err != nil {
			allowIP := net.ParseIP(allow)
			if allowIP == nil {
				return nil, errors.New(fmt.Sprintf("contain not a valid ip address: %s\n", err))
			}
			ips = append(ips, allowIP)
			continue
		}
		ipnet = append(ipnet, ipNet)
	}
	var srv = new(Server)
	srv.ServeMux = mux
	srv.IPS = ips
	srv.IPNET = ipnet
	srv.Logger = l
	srv.exit = make(chan int)
	return srv, nil
}

//检查ip权限
func (srv *Server) Allowed(ip net.IP) bool {
	for i := 0; i < len(srv.IPNET); i++ {
		if srv.IPNET[i].Contains(ip) {
			return true
		}
	}
	for j := 0; j < len(srv.IPS); j++ {
		if srv.IPS[j].Equal(ip) {
			return true
		}
	}
	return false
}

//实现ServeHTTP: 并检查IP地址是否允许访问
func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var ip string
	if xforwarfor := r.Header.Get("X-Forward-For"); xforwarfor != "" {
		ip = xforwarfor
		if srv.Allowed(net.ParseIP(xforwarfor)) {
			srv.ServeMux.ServeHTTP(w, r)
		}
		return
	}
	userip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		srv.Logger.Printf("client ip: %q is not IP:port", r.RemoteAddr)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	ip = userip
	if srv.Allowed(net.ParseIP(ip)) {
		srv.ServeMux.ServeHTTP(w, r)
		return
	}
	srv.Logger.Printf("client %s connect not allowed\n", ip)
	http.Error(w, fmt.Sprintf("ip no allowed: %s", ip), http.StatusForbidden)
}

func hideSecret(global *Global) *Global {
	glob := Global{
		Debug:     global.Debug,
		Heartbeat: global.Heartbeat,
		Interval:  global.Interval,
		Times:     global.Times,
		Mail: Mailer{
			MailFrom: global.Mail.MailFrom,
			RcptTo:   global.Mail.RcptTo,
			Secret:   "******",
			SmtpHost: global.Mail.SmtpHost,
			SmtpPort: global.Mail.SmtpPort,
		},
		RelayTime: global.RelayTime,
	}
	return &glob
}

//返回JSON对象: 隐藏密码
func config(w http.ResponseWriter, r *http.Request, l *log.Logger, jc *jsonconfig) {
	var lock sync.Mutex
	lock.Lock()
	if r.Method == "GET" {
		var b []byte
		var err error
		q := r.FormValue("g")
		switch q {
		case "global":
			b, err = json.MarshalIndent(hideSecret(jc.Global), "", "  ")
		case "groups":
			if areaid := r.FormValue("areaid"); areaid != "" {
				if gcfg, ok := jc.Groups[areaid]; ok {
					b, err = json.MarshalIndent(gcfg, "", "  ")
				} else {
					l.Printf("%s get group %s failed: id error", r.RemoteAddr, areaid)
				}
			} else {
				b, err = json.MarshalIndent(jc.Groups, "", "  ")
			}
		default:
			b, err = json.MarshalIndent(jsonconfig{hideSecret(jc.Global), jc.Groups}, "", "  ")
		}
		if err != nil {
			l.Println("json", err)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "parse json error")
			return
		}

		lock.Unlock()
		//获取jquery的getJSON生成的callback函数名称
		callback := r.FormValue("callback")
		var s string
		if callback != "" {
			//使用callback连接返回值: 格式: $callback("jsonconfig")
			s = fmt.Sprintf("%s(%s)", callback, b)
		} else {
			s = string(b)
		}
		l.Printf("client: %s 查询当前%s配置\n", r.RemoteAddr, q)
		//允许跨域请求
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.Header().Set("Content-Type", "text/json; charset=utf-8")
		fmt.Fprintf(w, "%s", s)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

//检查IP地址解析是否正常
func checkAddr(s string) error {
	_, err := net.ResolveIPAddr("ip", s)
	if err != nil {
		return err
	}
	return nil
}

/*
字符串s的格式:
name1: host1
name2, host2
name3, host3
*/
func splitHost(s string) ([]*jsonhost, error) {
	//清除空格
	s = strings.Replace(s, " ", "", -1)
	hs := strings.Split(s, "\n")

	if hs == nil {
		return nil, errors.New(fmt.Sprintf("hosts split host line: %s", s))
	}
	var jhs []*jsonhost
	for i, hline := range hs {
		//跳过空行
		if hline == "" {
			continue
		}
		//fmt.Println("hline", hline)
		host := strings.Split(hline, ",")
		//fmt.Println("host", host)
		if len(host) != 2 {
			return nil, errors.New(fmt.Sprintf("hosts line: %v, split host name and address: %s", i, hline))
		}
		for _, v := range jhs {
			//检查是否重复
			if v.Name == host[0] || v.Addr == host[1] {
				return nil, errors.New(fmt.Sprintf("hosts line %v: %s, name or address exists already:", i, hline))
			}
			if err := checkAddr(host[1]); err != nil {
				return nil, errors.New(fmt.Sprintf("hosts line %v: %s, address resolve failed", i, hline))
			}
		}
		jhs = append(jhs, &jsonhost{host[0], host[1]})
	}
	return jhs, nil
}

//接收post的全局参数, 并保存
func setting(w http.ResponseWriter, r *http.Request, l *log.Logger, jc *jsonconfig, cfgPath string) {
	if r.Method == "POST" {
		if err := r.ParseForm(); err != nil {
			l.Printf("[Error] client %s 更新全局参数, %s\n", r.RemoteAddr, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var global = jc.Global
		if interval := r.FormValue("interval"); interval != "" {
			d, err := time.ParseDuration(interval)
			if err != nil {
				l.Printf("[Error] client %s 更新 interval: %s, %s\n", r.RemoteAddr, interval, err)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "interval: %s", interval)
				return
			}
			if def_d := time.Second * time.Duration(mini_interval); d < def_d {
				global.Interval = def_d.String()
			} else {
				global.Interval = interval
			}
		}
		if times := r.FormValue("times"); times != "" {
			if re.MatchString(times) {
				l.Printf("[Error] client %s 更新 times: %s, times must be digit\n", r.RemoteAddr, times)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "times: %s not a number", times)
				return
			}
			x, err := strconv.Atoi(times)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "times: %s", times)
				return
			}
			if x < mini_times {
				global.Times = mini_times
			} else {
				global.Times = x
			}
		}
		if rcpt := r.FormValue("rcpt_to"); rcpt != "" {
			if _, err := mail.ParseAddress(rcpt); err != nil {
				l.Printf("[Error] client %s 更新全局接收邮箱 %s\n", r.RemoteAddr, rcpt)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `Save global Setting failed, email is %s`, rcpt)
				return
			}
			global.Mail.RcptTo = rcpt
		}
		if from := r.FormValue("mail_from"); from != "" {
			if _, err := mail.ParseAddress(from); err != nil {
				l.Printf("[Error] client %s 更新全局发送邮箱 %s\n", r.RemoteAddr, from)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `Save global Setting failed, from is %s`, from)
				return
			}
			global.Mail.MailFrom = from
		}
		if secret := r.FormValue("secret"); secret != "" {
			global.Mail.Secret = secret
		}
		if smtpHost := r.FormValue("smtp_server"); smtpHost != "" {
			global.Mail.SmtpHost = r.FormValue("smtp_server")
		}
		if smtpPort := r.FormValue("smtp_port"); smtpPort != "" {
			global.Mail.SmtpPort = r.FormValue("smtp_port")
		}
		if relayTime := r.FormValue("relay_time"); relayTime != "" {
			relay, err := strconv.Atoi(relayTime)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "relay_time: %s", relayTime)
				return
			}
			if relay < 1 || relay > 60 {
				relay = 30
			}
			global.RelayTime = relay
		}

		err := JsonConfigWrite(cfgPath, global, true)
		if err != nil {
			l.Printf("[Error] client %s 更新全局配置失败, %s\n", r.RemoteAddr, err)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, err)
			return
		}

		l.Printf("[Success] client %s 更新的全局配置成功\n", r.RemoteAddr)

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Content-Type", "text/json; charset=utf-8")
		fmt.Fprint(w, `{"SaveGlobalConfigSetting":"OK"}`)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, "please use post method")
	}
}

//接受group配置: 并保存
func groupSetting(w http.ResponseWriter, r *http.Request, l *log.Logger, jc *jsonconfig, gdir string) {
	if r.Method == "POST" {
		if err := r.ParseForm(); err != nil {
			l.Printf("[Error] client %s 更新主机参数, %s\n", r.RemoteAddr, err)
			l.Println(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		area := r.FormValue("area")
		if area == "" {
			l.Printf("[Error] client %s 更新主机参数, area 不能为空\n", r.RemoteAddr)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `Save Group Setting failed, area is ""`)
			return
		}
		g, ok := jc.Groups[area]
		if !ok {
			l.Printf("[Error] client %s 更新主机参数, area: %s 参数: 配置文件中没有此area\n", r.RemoteAddr, area)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `Save Group Setting failed, area is not in configure`)
			return
		}
		name := r.FormValue("name")
		if name == "" {
			l.Printf("[Error] client %s 更新主机参数, name 不能为空\n", r.RemoteAddr)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `Save Group Setting failed, name is ""`)
			return
		}
		g.Name = name

		hs, err := splitHost(r.FormValue("hosts"))
		if err != nil {
			l.Printf("[Error] client %s 更新主机列表, area: %s, %s\n", r.RemoteAddr, area, err)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "%s", err)
			return
		}
		g.Hosts = hs

		//检查邮箱名
		if email := r.FormValue("email"); email != "" {
			if _, err := mail.ParseAddress(email); err != nil {
				l.Printf("[Error] client %s 更新邮箱, area: %s, %s\n", r.RemoteAddr, area, email)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `Save Group Setting failed, email is %s`, email)
				return
			}
			g.Email = email
		}

		//备份组配置文件: 重命名为.back
		if err := JsonConfigWrite(g.path, g, true); err != nil {
			l.Printf("[Error] client %s 更新区域 %s 时: 配置备份失败 %s", r.RemoteAddr, area, err)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, err)
			return
		}
		l.Printf("[Success] client %s 更新区域 %s 配置成功\n", r.RemoteAddr, area)

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Content-Type", "text/json; charset=utf-8")
		fmt.Fprint(w, `{"groupSetting":"OK"}`)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, "please use post method")
	}
}

//runCmd执行子命令: 端口为ping守护进程提供status查询的http端口
func (srv *Server) runCmd(name, port string, l *log.Logger) error {
	cmd := exec.Command(name, "-port", port, "run")
	if err := cmd.Start(); err != nil {
		return err
	}
	srv.cmd = &command{localPort: port, logger: l}
	srv.cmd.Process = cmd.Process
	//启动一个线程等待退出
	go func(s *Server) {
		s.cmd.Wait()
		s.exit <- 1
	}(srv)

	go func(s *Server) {
		for range s.exit {
			s.cmd.Process = nil
		}
	}(srv)
	return nil
}

//实现Web中的请求ping守护子进程PID和启动、停止操作
func process(w http.ResponseWriter, r *http.Request, srv *Server, l *log.Logger, port string) {
	switch r.Method {
	case "GET":
		if srv.cmd.Process != nil {
			fmt.Fprintf(w, "%v", srv.cmd.Pid)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "%v", srv.cmd)
		}
		return
	case "POST":
		if err := r.ParseForm(); err != nil {
			l.Println(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		l.Printf("[Warn] client: %s %s ping service\n", r.RemoteAddr, r.FormValue("todo"))
		switch todo := r.FormValue("todo"); todo {
		case "start":
			if srv.cmd.Process == nil {
				if err := srv.runCmd(os.Args[0], port, l); err != nil {
					l.Printf("start deamon error: %s\n", err)
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintln(w, err)
				} else {
					l.Println("start deamon success")
					w.WriteHeader(http.StatusOK)
					fmt.Fprintln(w, "ok")
				}
			} else {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintln(w, "proccess running already")
			}
		case "stop":
			if srv.cmd.Process != nil {
				if err := srv.cmd.Kill(); err != nil {
					l.Printf("stop deamon error: %s\n", err)
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintln(w, err)
				} else {
					l.Println("stopped deamon success")
					w.WriteHeader(http.StatusOK)
					fmt.Fprintln(w, "ok")
				}
			} else {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintln(w, "proccess not running")
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "no method %s", todo)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintln(w, "please use post method")
	}
}

func logs(w http.ResponseWriter, r *http.Request, l *log.Logger, pl string) {
	if r.Method == "GET" {
		b, err := ioutil.ReadFile(pl)
		if err != nil {
			l.Printf("[Error] client %s read %s: %s\n", r.RemoteAddr, pl, err)
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, err)
			return
		}
		//l.Printf("client %s read %s\n", r.RemoteAddr, pl)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, string(b))
	} else {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, "please use get method")
	}
}

func (srv *Server) Listen(addr string, jcfg *jsonconfig, l *log.Logger, port, cfgPath, gDir, pingLogFilePath, httpLogFile string) {
	srv.HandleFunc("/admin/setting", func(w http.ResponseWriter, r *http.Request) {
		config(w, r, l, jcfg)
	})

	ls, err := url.Parse(fmt.Sprintf("http://%s:%s", localserver, port))
	if err != nil {
		log.Fatalln(err)
	}
	//反向代理goping的status查询-->子进程的地址端口
	proxy := httputil.NewSingleHostReverseProxy(ls)
	proxy.ErrorLog = l
	srv.Handle("/status", proxy)

	srv.HandleFunc("/admin/setting/global", func(w http.ResponseWriter, r *http.Request) {
		setting(w, r, l, jcfg, cfgPath)
	})
	srv.HandleFunc("/admin/setting/group", func(w http.ResponseWriter, r *http.Request) {
		groupSetting(w, r, l, jcfg, gDir)
	})

	srv.runCmd(os.Args[0], port, l)

	srv.HandleFunc("/admin/process", func(w http.ResponseWriter, r *http.Request) {
		process(w, r, srv, l, port)
	})

	srv.HandleFunc("/logs/ping", func(w http.ResponseWriter, r *http.Request) {
		b, err := ioutil.ReadFile(pingLogFilePath)
		if err != nil {
			l.Printf("[Error] client %s read %s: %s\n", r.RemoteAddr, pingLogFilePath, err)
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, err)
		} else {
			logs(w, r, l, string(b))
		}
	})
	srv.HandleFunc("/logs/http", func(w http.ResponseWriter, r *http.Request) {
		logs(w, r, l, httpLogFile)
	})
	//静态文件: jquery和bootstrap等文件
	html := BaseDir() + "/html"
	srv.Handle("/", http.FileServer(http.Dir(html)))

	log.Fatalln(http.ListenAndServe(addr, srv))
}
