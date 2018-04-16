package main

import (
    "os"
    "strconv"
    "strings"
    "flag"
    "bytes"
    "log"
    "net"
    "gopkg.in/telegram-bot-api.v4"
    "github.com/spf13/viper"
    "github.com/veqryn/go-email/email"
    "./smtpd"
)

var receivers map[string]string
var bot *tgbotapi.BotAPI
var debug bool

func main() {

    configFilePath := flag.String("c", "./smtp2tg.toml", "Config file location")
    //pidFilePath := flag.String("p", "/var/run/smtp2tg.pid", "Pid file location")
    flag.Parse()
    
    // Load & parse config
    viper.SetConfigFile(*configFilePath)
    err := viper.ReadInConfig()
    if( err != nil ) {
	log.Fatal(err.Error())
    }
    
    // Logging
    logfile := viper.GetString("logging.file")
    if( logfile == "" ) {
	log.Println("No logging.file defined in config, outputting to stdout")
    } else {
	lf, err := os.OpenFile(logfile, os.O_APPEND | os.O_CREATE | os.O_RDWR, 0666)
	if( err != nil ) {
	    log.Fatal(err.Error())
	}
	log.SetOutput(lf)
    }
    
    // Debug?
    debug = viper.GetBool("logging.debug")
    
    
    receivers = viper.GetStringMapString("receivers")
    if( receivers["*"] == "" ) {
	log.Fatal("No wildcard receiver (*) found in config.")
    }
    
    var token string = viper.GetString("bot.token")
    if( token == "" ) {
	log.Fatal("No bot.token defined in config")
    }
    
    var listen string = viper.GetString("smtp.listen")
    var name string = viper.GetString("smtp.name")
    if( listen == "" ) {
	log.Fatal("No smtp.listen defined in config.")
    }
    if( name == "" ) {
	log.Fatal("No smtp.name defined in config.")
    }
    
    // Initialize TG bot
    bot, err = tgbotapi.NewBotAPI( token )
    if( err != nil ) {
	log.Fatal(err.Error())
    }
    log.Printf("Bot authorized as %s", bot.Self.UserName )
    
    
    log.Printf("Initializing smtp server on %s...", listen)
    // Initialize SMTP server
    err_ := smtpd.ListenAndServe(listen, mailHandler, "mail2tg", "", debug)
    if( err_ != nil ) {
	log.Fatal(err_.Error())
    }
}
    
func mailHandler(origin net.Addr, from string, to []string, data []byte) {
    
    from = strings.Trim(from, " ")
    to[0] = strings.Trim(to[0], " ")
    to[0] = strings.Trim(to[0], "<")
    to[0] = strings.Trim(to[0], ">")
    msg, err := email.ParseMessage(bytes.NewReader(data))
    if( err != nil ) {
	log.Printf("[MAIL ERROR]: %s", err.Error())
	return
    }
    subject := msg.Header.Get("Subject")
    log.Printf("Received mail from '%s' for '%s' with subject '%s'", from, to[0], subject)
    
    // Find receivers and send to TG
    var tgid string
    if( receivers[to[0]] != "" ) {
	tgid = receivers[to[0]]
    } else {
	tgid = receivers["*"]
    }
    
    textMsgs := msg.MessagesContentTypePrefix("text")
    images := msg.MessagesContentTypePrefix("image")
    if len(textMsgs) == 0 && len(images) == 0 {
        log.Printf("mail doesn't contain text or image")
	    return    
    }

    log.Printf("Relaying message to: %v", tgid)
    
    i, err := strconv.ParseInt(tgid, 10, 64)
    if( err != nil ) {
	log.Printf("[ERROR]: wrong telegram id: not int64")
	return
    }
    
    if len(textMsgs) > 0 {
        bodyStr := string(textMsgs[0].Body)
        tgMsg := tgbotapi.NewMessage(i, bodyStr)
        tgMsg.ParseMode = tgbotapi.ModeMarkdown
        _, err = bot.Send(tgMsg)
        if err != nil {
            log.Printf("[ERROR]: telegram message send: '%s'", err.Error())
            return
        }
    }

    // TODO Better to use 'sendMediaGroup' to send all attachments as a
    // single message, but go telegram api has not implemented it yet
    // https://github.com/go-telegram-bot-api/telegram-bot-api/issues/143    
    for _, part := range msg.MessagesContentTypePrefix("image") {
        _, params, err := part.Header.ContentDisposition()
        if err != nil {
            log.Printf("[ERROR]: content disposition parse: '%s'", err.Error())
            return
        }
        text := params["filename"]
        tgFile := tgbotapi.FileBytes{Name: text, Bytes: part.Body}
        tgMsg := tgbotapi.NewPhotoUpload(i, tgFile)
        tgMsg.Caption = text
        // It's not a separate message, so disable notification
        tgMsg.DisableNotification = true
        _, err = bot.Send(tgMsg)
        if err != nil {
            log.Printf("[ERROR]: telegram photo send: '%s'", err.Error())
            return
        }
    }
}
