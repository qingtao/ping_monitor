// monitor
// 提供对服务器或者网络设备ping监测
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

//版本信息
const version = "v20170228"

var (
	//配置文件路径
	//conf = flag.String("config", "", "配置文件路径")
	//http服务地址端口
	httpAddr = flag.String("addr", ":50051", "WEB服务地址")
	//主机状态查询端口
	port = flag.String("port", "50052", "本地状态查询端口")
	//临时目录
	exampleDir = "example"

	mini_interval = 30
	mini_times    = 3
)

//一个简单的配置例子，用于初始化配置文件
var configExample = jsonconfig{
	Global: &Global{
		Mail: Mailer{
			//接收人
			RcptTo: "to@example.com",
			//发件人
			MailFrom: "send@example.com",
			//发件人密码
			Secret: "password",
			//邮件服务器地址
			SmtpHost: "smtp.example.com",
			//邮件服务器端口
			SmtpPort: "25",
		},
		RelayTime: 30,
		Heartbeat: "baidu.com",
		//时间间隔
		Interval: "30s",
		//失败次数
		Times: 3,
	},
	Groups: map[string]*Group{
		"shandong": &Group{
			Area:  "shandong",
			Name:  "山东",
			Email: "mail@example.com",
			Hosts: []*jsonhost{
				{
					Name: "host1",
					Addr: "192.168.1.1",
				},
				{
					Name: "host2",
					Addr: "192.168.1.2",
				},
			},
		},
	},
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()

	baseDir := BaseDir()
	logDir := filepath.Join(baseDir, "logs")
	tmpDir := filepath.Join(baseDir, "tmp")
	groupDir := filepath.Join(baseDir, "etc", "conf.d")

	Mkdir(logDir)
	Mkdir(tmpDir)
	Mkdir(groupDir)

	var conf = filepath.Clean(filepath.Join(baseDir, "etc", "config.json"))
	//读取配置文件
	jcfg, err := JsonConfigReader(conf, groupDir)
	if err != nil {
		log.Fatalln("read configure error: ", err)
	}

	cfg := ConfigReader(jcfg)
	argv := flag.Args()
	if len(argv) < 1 {
		fmt.Println("argv less than one")
		return
	}

	var date = time.Now().Format(`200601021504`)
	var pingLogFilePath = filepath.Join(tmpDir, "ping.txt")

	switch argv[0] {
	//输出版本信息
	case "version":
		fmt.Printf("%s version %s\n", os.Args[0], version)

		return
	//生成配置文件样例
	case "gen":
		exampleDir = filepath.Join(baseDir, exampleDir)
		Mkdir(exampleDir)
		if err := configExample.Save(exampleDir, false); err != nil {
			log.Fatalln(err)
		}
		fmt.Printf("generate configure files (json格式）in path:\n%s\n",
			exampleDir)
		return
	//运行http服务
	case "http":
		var httpLogFile = filepath.Join(logDir, "http_"+date+".log")
		var httpLog = Log(httpLogFile)

		wf := filepath.Join(baseDir, "etc", "whitelist")
		srv, err := NewServer(wf, httpLog)
		if err != nil {
			log.Fatalln("read whitelist error: ", err)
		}
		pid, pidFile := os.Getpid(), filepath.Join(tmpDir, "http.pid")
		httpLog.Printf("Version: %s\n", version)
		httpLog.Printf("pid: %d, file: %s\n", pid, pidFile)

		if err := ioutil.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0666); err != nil {
			httpLog.Printf("pid: %s, error: %s\n", pid, err)
			return
		}

		//启动http服务
		srv.Listen(*httpAddr, jcfg, httpLog, *port, conf, groupDir, pingLogFilePath, httpLogFile)

	case "run":
		var pingLogFile = filepath.Join(logDir, "ping_"+date+".log")
		var pingLog = Log(pingLogFile)

		if err = ioutil.WriteFile(pingLogFilePath, []byte(pingLogFile), 0666); err != nil {
			pingLog.Printf("write date to file %s error: %s\n", pingLogFilePath, err)
			return
		}

		//打印基本信息到日志
		pingLog.Printf("Version: %s\n", version)
		pingLog.Println("Monitor daemon is starting...")
		pid, pidFile := os.Getpid(), filepath.Join(tmpDir, "ping.pid")
		pingLog.Printf("pid: %d, file: %s\n", pid, pidFile)

		if err := ioutil.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0666); err != nil {
			pingLog.Printf("write pid: %s\n", err)
			return
		}
		/*
			for i := 0; i < len(cfg.Hosts); i++ {
				pingLog.Printf("name: [%s] - address: [%s]\n", cfg.Hosts[i].Name, cfg.Hosts[i].Addr)
			}
		*/

		//创建监控实例
		m := NewMonitor(cfg, pingLog)
		//启动goroutine等待发送邮件
		go m.Notify()
		//启动邮件事件接收goroutine
		go m.resv()
		//提供主机状态JSON
		go m.status(fmt.Sprintf("%s:%s", localserver, *port))
		//启动监控
		m.start()
	default:
		Usage := `Usage:
  %s [Options] [version | run | gen | http]
  http: 可以与-addr选项一起使用
  run： 可以与-port选项一起使用

Options:
`
		fmt.Printf(Usage, os.Args[0])
		flag.PrintDefaults()
	}

	fmt.Println(`
whitelist default only allow:
    127.0.0.1
    ::1
`)
	fmt.Println(`Note:
    when -addr="127.0.0.1:50051"
    nginx.conf:
    location /monitor/ {
        proxy_pass http://127.0.0.1:50051/;
        proxy_set_header X-Forward-For $remote_addr;
        proxy_set_header Host $host;
    }
`)
}
