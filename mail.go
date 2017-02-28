package main

import (
	"encoding/base64"
	"fmt"
	"net/mail"
	"net/smtp"
	"time"
)

func (m *monitor) Notify() {
	for k, v := range m.cfg.MailResv {
		m.logger.Printf("[INFO] starting process wait for %s\n", k)
		go func(name string, ch <-chan *Host) {
		TOP:
			for newhost := range ch {
				var hs = []*Host{newhost}
				for {
					select {
					case h := <-ch:
						hs = append(hs, h)
					case <-time.After(time.Duration(m.cfg.RelayTime) * time.Second):
						if err := SendMail(m.cfg.Mail, hs); err != nil {
							m.logger.Printf("[ERROR] send notify email of %s %s\n", name, err)
						} else {
							m.logger.Printf("[INFO] send email of %s ok\n", name)
						}
						continue TOP
					}
				}
				/*if err := SendMail(m.cfg.Mail, hs); err != nil {
					m.logger.Printf("[ERROR] send notify email of %s %s\n", name, err)
				} else {
					m.logger.Printf("[INFO] send email of %s ok\n", name)
				}
				*/
			}
		}(k, v)
	}
}

func SendMail(m Mailer, hs []*Host) error {
	b64 := base64.StdEncoding

	from, err := mail.ParseAddress(m.MailFrom)
	if err != nil {
		return err
	}

	rcpt := m.RcptTo
	h1 := hs[0]

	if m.Emails[h1.AreaID] != "" {
		rcpt = rcpt + "," + m.Emails[h1.AreaID]
	}
	rcpts, err := mail.ParseAddressList(rcpt)
	if err != nil {
		return err
	}

	var to = make([]string, 0)
	for i := 0; i < len(rcpts); i++ {
		to = append(to, rcpts[i].Address)
	}

	header := make(map[string]string)
	header["From"] = from.String()
	header["To"] = rcpt

	var body string
	var format = "2006-01-02 15:04:05"

	for i, v := range hs {
		status := `<span style="color: red;">离线</span>`
		if v.Stat {
			status := `<span style="color: green;">上线</span>`
			body += fmt.Sprintf(`<div>%d、%s：%s %s<br /> 恢复时间: %s</div>`,
				i+1, v.Name, v.Addr, status, v.Last.Format(format))
		} else {
			body += fmt.Sprintf(`<div>%d、%s：%s %s<br /> 最近在线: %s</div>`,
				i+1, v.Name, v.Addr, status, v.Last.Format(format))
		}
	}

	subject := "网络设备状态变化通知"
	//utf8
	header["Subject"] = fmt.Sprintf("=?UTF-8?B?%s?=",
		b64.EncodeToString([]byte(fmt.Sprintf("[%s] %s - %s", h1.AreaID, h1.Area, subject))))
	header["MIME-Version"] = "1.0"
	header["Content-Type"] = "text/html; charset=UTF-8"
	header["Content-Transfer-Encoding"] = "base64"

	msg := ""

	for k, v := range header {
		msg += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	msg += "\r\n" + b64.EncodeToString([]byte(body))

	auth := smtp.PlainAuth(
		"",
		from.Address,
		m.Secret,
		m.SmtpHost,
	)

	err = smtp.SendMail(
		m.SmtpHost+":"+m.SmtpPort,
		auth,
		from.Address,
		to,
		[]byte(msg),
	)
	if err != nil {
		return err
	}
	return nil
}
