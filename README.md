# goping
对服务器或者网络设备ping监测

last update：20160309

###本程序提供对服务器或者网络设备ping监测：
使用方法：

1. 初始化：使用命令

  monitor example
  
  生成config.example.json文件，然后修改名称为config.json
2. linux下使用启动脚本管理，如debian下：

  sudo cp -R monitor /usr/local/
  
  sudo cp bin/goping /etc/init.d/goping
  
  sudo insserv goping
  
3. 定时重启:cron.d/goping

  35 8 * * 1 root /etc/init.d/goping restart > /dev/nulll 2>&1

