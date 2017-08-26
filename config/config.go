package config

import (
	"errors"
	"io/ioutil"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-fsnotify/fsnotify"
	yaml "gopkg.in/yaml.v2"
)

type Config struct {
	Action []Action
	Shell  []string
}

type Action struct {
	Name           string
	On             []string
	Interval       string
	IntervalAction []IntervalAction `yaml:"interval_action"`
	Run            string
}

type IntervalAction struct {
	On []string
	Do string
}

func LoadConfig(path string) (*Config, error) {
	buf, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Default values
	var conf Config
	if runtime.GOOS == "windows" {
		conf.Shell = []string{"cmd.exe", "/c"}
	} else {
		conf.Shell = []string{"bash", "-c"}
	}

	err = yaml.Unmarshal(buf, &conf)
	if err != nil {
		return nil, err
	}

	err = validateConfig(&conf)
	if err != nil {
		return nil, err
	}

	return &conf, nil
}

func validateConfig(conf *Config) error {
	if len(conf.Action) == 0 {
		return errors.New("No actions were defined")
	}
	for _, action := range conf.Action {
		if err := validateActionConfig(&action); err != nil {
			return err
		}
	}

	return nil
}

func validateActionConfig(action *Action) error {
	if action.Name == "" {
		return errors.New("action's 'name' is empty")
	}

	for i, on := range action.On {
		if on != "write" &&
			on != "create" &&
			on != "remove" &&
			on != "rename" &&
			on != "chmod" {
			return errors.New(on + ": action[].on[" + strconv.Itoa(i) + "] is invalid value")
		}
	}

	_, err := parseIntervalMSec(action.Interval)
	if err != nil {
		return err
	}

	err = validateIntervalAction(action.IntervalAction)
	if err != nil {
		return err
	}

	if action.Run == "" {
		return errors.New("action's 'run' is empty")
	}
	return nil
}

var rxOn = regexp.MustCompile(`^!?(self|write|create|remove|rename|chmod)$`)

func validateIntervalAction(intervalAction []IntervalAction) error {
	for i, iaction := range intervalAction {
		for j, on := range iaction.On {
			if !rxOn.MatchString(on) {
				return errors.New("'interval_action[" + strconv.Itoa(i) + "].on[" +
					strconv.Itoa(j) + "]' is invalid value (either \"self\", \"write\", " +
					"\"create\", \"remove\", \"rename\", \"chmod\")")
			}
		}
		if iaction.Do != "ignore" && iaction.Do != "cancel" && iaction.Do != "retry" {
			return errors.New("'interval_action[" + strconv.Itoa(i) + "].do' is invalid value " +
				"(\"ignore\" or \"cancel\" or \"retry\")")
		}
	}
	return nil
}

var intervalPattern = regexp.MustCompile(`^0*(\d+)(m?s(ec)?)?$`)

func parseIntervalMSec(interval string) (int64, error) {
	switch interval {
	case "":
		fallthrough
	case "0":
		fallthrough
	case "0s":
		fallthrough
	case "0sec":
		fallthrough
	case "0ms":
		fallthrough
	case "0msec":
		return 0, nil
	}
	result := intervalPattern.FindStringSubmatch(interval)
	if len(result) == 0 {
		return 0, errors.New(interval + ": 'interval' is invalid value")
	}
	msec, err := strconv.ParseInt(result[1], 10, 64)
	if err != nil {
		return 0, err
	}
	if result[2] == "" && result[1] != "0" {
		return 0, errors.New(interval + ": must specify unit to 'interval' except '0'")
	}
	if result[2] == "sec" || result[2] == "s" {
		msec = msec * 1000
	}
	return msec, nil
}

func MustParseIntervalMSec(interval string) int64 {
	msec, err := parseIntervalMSec(interval)
	if err != nil {
		panic(err)
	}
	return msec
}

type ActionType uint32

const (
	Ignore ActionType = iota
	Retry
	Cancel
)

func (action *Action) DetermineIntervalAction(
	selfOp fsnotify.Op,
	newOp fsnotify.Op,
	elseValue ActionType) (ActionType, error) {

	intervalAction := action.IntervalAction
	for _, iaction := range intervalAction {
		for _, on := range iaction.On {
			var invert bool
			var op fsnotify.Op
			if strings.HasPrefix(on, "!") {
				invert = true
				var err error
				op, err = convertEventNameToOp(on[1:], selfOp)
				if err != nil {
					return Ignore, err
				}
			} else {
				invert = false
				var err error
				op, err = convertEventNameToOp(on, selfOp)
				if err != nil {
					return Ignore, err
				}
			}
			if !invert && op == newOp || invert && op != newOp {
				do, err := parseActionDo(iaction.Do)
				if err != nil {
					return Ignore, err
				}
				return do, nil
			}
		}
	}
	return elseValue, nil
}

func convertEventNameToOp(eventName string, selfOp fsnotify.Op) (fsnotify.Op, error) {
	if eventName == "self" {
		return selfOp, nil
	} else if eventName == "write" {
		return fsnotify.Write, nil
	} else if eventName == "create" {
		return fsnotify.Create, nil
	} else if eventName == "remove" {
		return fsnotify.Remove, nil
	} else if eventName == "rename" {
		return fsnotify.Rename, nil
	} else if eventName == "chmod" {
		return fsnotify.Chmod, nil
	} else {
		return fsnotify.Write, errors.New(
			"Can't convert '" + eventName + "' to fsnotify.Op")
	}
}

func parseActionDo(do string) (ActionType, error) {
	if do == "ignore" {
		return Ignore, nil
	} else if do == "retry" {
		return Retry, nil
	} else if do == "cancel" {
		return Cancel, nil
	} else {
		return Ignore, errors.New(do + ": invalid 'interval_action[].do'")
	}
}

func (config *Config) GetActionsOn(target string) []*Action {
	actions := make([]*Action, 0, len(config.Action))
	for i := range config.Action {
		action := &config.Action[i]
		for _, on := range action.On {
			if on == target {
				actions = append(actions, action)
				break
			}
		}
	}
	return actions
}
