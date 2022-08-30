package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"banyanhills.com/leaf/util/client"
	"bitbucket.org/banyanhills/leaf_core/messaging"
	"bitbucket.org/banyanhills/leaf_core/util"
	log "github.com/cihub/seelog"
)

type CommandConfig struct {
	Name               string `json:"name"`
	Path               string `json:"path"`
	Alias              string `json:"alias"`
	IncludeCommandName bool   `json:"includeCommandName"`
	Timeout            int    `json:"timeout"`
}

type CommandProcessorConfig struct {
	Commands []CommandConfig
}

type Command struct {
	Command    string
	Id         string
	Params     string
	ParamsList []string
}
type CommandProcessor struct {
	commandQueue []Command
	commandMap   map[string]CommandConfig
	running      bool
	// sendMessageChannel    chan messaging.Message
	mtx                   sync.Mutex
	defaultCommandTimeout int
}

func (p *CommandProcessor) ProcessCommands(serviceCaller string) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("run time panic: %v", r)
			panic(r)
		}
	}()
	p.running = true
	for p.running {
		p.mtx.Lock()
		if len(p.commandQueue) == 0 {
			p.mtx.Unlock()
			time.Sleep(time.Duration(1) * time.Second)
			continue
		}
		command := p.commandQueue[0]
		p.commandQueue = p.commandQueue[1:]
		p.mtx.Unlock()
		log.Debugf("Executing command: %+v", command)

		cmdConfig, ok := p.commandMap[command.Command]
		if !ok {
			log.Errorf("No command %s configured for this "+serviceCaller+"agent service", command.Command)
			failedMessage := messaging.Message{
				"messageType": "event",
				"eventType":   serviceCaller + ".command.failed",
				"data": messaging.Message{
					"commandId": command.Id,
					"error":     fmt.Sprintf("No command %s configured for this "+serviceCaller+" agent service", command.Command),
					"output":    "",
				},
			}
			dump(failedMessage)
			leafMessageBytes, err := json.Marshal(&failedMessage)
			if err != nil {
				log.Debugf("error marshal failed message %w", err)
			}
			client.SendMessageToAgent(leafMessageBytes)
			// p.sendMessageChannel <- failedMessage
			continue
		}
		if cmdConfig.IncludeCommandName {
			newParamsList := []string{cmdConfig.Name}
			if cmdConfig.Alias != "" {
				newParamsList = []string{cmdConfig.Alias}
			}
			command.ParamsList = append(newParamsList, command.ParamsList...)
		}
		timeout := p.defaultCommandTimeout
		if cmdConfig.Timeout > 0 {
			timeout = cmdConfig.Timeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		fmt.Println(cmdConfig.Path)
		fmt.Println(command.ParamsList)
		cmd := exec.CommandContext(ctx, cmdConfig.Path, command.ParamsList...)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			log.Errorf("Command %s failed with error %+v",
				cmdConfig.Path+" "+strings.Join(command.ParamsList, " "), err)
			failedMessage := messaging.Message{
				"messageType": "event",
				"eventType":   serviceCaller + ".command.failed",
				"data": messaging.Message{
					"commandId": command.Id,
					"error":     err.Error(),
					"output":    string(output),
				},
			}
			// p.sendMessageChannel <- failedMessage
			dump(failedMessage)
		} else {
			log.Infof("Command %s succeeded",
				cmdConfig.Path+" "+strings.Join(command.ParamsList, " "))
			succeededMessage := messaging.Message{
				"messageType": "event",
				"eventType":   serviceCaller + "command.succeeded",
				"data": messaging.Message{
					"commandId": command.Id,
					"output":    string(output),
				},
			}
			// p.sendMessageChannel <- succeededMessage
			dump(succeededMessage)
		}
	}
}

func ParseParamsList(cmd Command) (Command, error) {
	if len(cmd.Params) > 0 {
		paramsObjs := []interface{}{}
		d := json.NewDecoder(strings.NewReader(cmd.Params))
		d.UseNumber()
		err := d.Decode(&paramsObjs)
		if err != nil {
			return cmd, err
		}
		cmd.ParamsList = []string{}
		for _, param := range paramsObjs {
			strParam, ok := param.(string)
			if ok {
				cmd.ParamsList = append(cmd.ParamsList, strParam)
			} else {
				paramBytes, err := json.Marshal(&param)
				if err != nil {
					log.Debugf("Error re-marshaling parameter %+v: %+v", param, err)
					return cmd, err
				}
				cmd.ParamsList = append(cmd.ParamsList, string(paramBytes))
			}

		}
	} else {
		cmd.ParamsList = []string{}
	}
	return cmd, nil
}

func (p *CommandProcessor) ScheduleCommand(cmd Command, serviceCaller string) error {

	cmd, err := ParseParamsList(cmd)
	if err != nil {
		fmt.Println("nag error here")
		fmt.Println(err)
		failedMessage := messaging.Message{
			"messageType": "event",
			"eventType":   serviceCaller + ".command.failed",
			"data": messaging.Message{
				"commandId": cmd.Id,
				"error":     err.Error(),
			},
		}
		dump(failedMessage)
		leafMessageBytes, err := json.Marshal(&failedMessage)
		if err != nil {
			log.Debugf("error marshal failed message %w", err)
		}
		client.SendMessageToAgent(leafMessageBytes)
		// p.sendMessageChannel <- failedMessage
		return err
	}

	_, ok := p.commandMap[cmd.Command]
	if !ok {
		fmt.Println("nag error wla ni sya sa command config @@@@@")
		log.Errorf("No command %s configured for this "+serviceCaller+" agent service", cmd.Command)
		failedMessage := messaging.Message{
			"messageType": "event",
			"eventType":   serviceCaller + ".command.failed",
			"data": messaging.Message{
				"commandId": cmd.Id,
				"error":     fmt.Sprintf("No command %s configured for this "+serviceCaller+" agent service", cmd.Command),
				"output":    "",
			},
		}
		dump(failedMessage)
		leafMessageBytes, err := json.Marshal(&failedMessage)
		if err != nil {
			log.Debugf("error marshal failed message %w", err)
		}
		client.SendMessageToAgent(leafMessageBytes)
		// p.sendMessageChannel <- failedMessage
		return fmt.Errorf("error command name:%s not found", cmd.Command)
	}

	p.mtx.Lock()
	p.commandQueue = append(p.commandQueue, cmd)
	p.mtx.Unlock()

	return nil
}

func (p *CommandProcessor) Stop() {
	p.running = false
}

func (p *CommandProcessor) Start(serviceCaller string) {
	go p.ProcessCommands(serviceCaller)
}

func MakeCommandProcessor(config *CommandProcessorConfig, defaultCommandTimeout int) *CommandProcessor {
	cp := CommandProcessor{
		commandMap:            map[string]CommandConfig{},
		defaultCommandTimeout: defaultCommandTimeout}
	for _, cmd := range config.Commands {
		cmd.Path = UpdateCommandPaths(cmd.Path)
		cp.commandMap[cmd.Name] = cmd
	}
	return &cp
}

func UpdateCommandPaths(command string) string {
	command = strings.Replace(command, "${LEAF_DIR}", util.LeafDir(), -1)
	exe := ""
	script := ".sh"
	if runtime.GOOS == "windows" {
		exe = ".exe"
		script = ".bat"
	}
	command = strings.Replace(command, "${EXE}", exe, -1)
	command = strings.Replace(command, "${SCRIPT}", script, -1)

	if strings.HasPrefix(command, "${BUILTIN_EXE(") {
		pos := strings.Index(command, "${BUILTIN_EXE(")
		startPos := pos + 14
		endPos := startPos + 1
		for endPos < len(command) && command[endPos] != ')' {
			endPos++
		}
		command = filepath.Join(util.LeafDir(), command[startPos:endPos]+exe)
	}
	if strings.HasPrefix(command, "${BUILTIN_SCRIPT(") {
		pos := strings.Index(command, "${BUILTIN_SCRIPT(")
		startPos := pos + 17
		endPos := startPos + 1
		for endPos < len(command) && command[endPos] != ')' {
			endPos++
		}
		command = filepath.Join(util.LeafDir(), command[startPos:endPos]+script)
	}
	return command
}
func dump(data interface{}) {
	b, _ := json.MarshalIndent(data, "", "  ")
	fmt.Print(string(b))
}
