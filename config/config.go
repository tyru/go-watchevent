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
	OnWrite  string `yaml:"on_write"`
	OnCreate string `yaml:"on_create"`
	OnRemove string `yaml:"on_remove"`
	OnRename string `yaml:"on_rename"`
	OnChmod  string `yaml:"on_chmod"`
	Action   []Action
	Shell    []string
}

type Action struct {
	Name           string
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

	if conf.OnWrite == "" &&
		conf.OnCreate == "" &&
		conf.OnRemove == "" &&
		conf.OnRename == "" &&
		conf.OnChmod == "" {
		return errors.New("No event(s) were specified")
	}

	if conf.OnWrite != "" && conf.LookupAction(conf.OnWrite) == nil {
		return errors.New("Action '" + conf.OnWrite + "' is not defined")
	}
	if conf.OnCreate != "" && conf.LookupAction(conf.OnCreate) == nil {
		return errors.New("Action '" + conf.OnCreate + "' is not defined")
	}
	if conf.OnRemove != "" && conf.LookupAction(conf.OnRemove) == nil {
		return errors.New("Action '" + conf.OnRemove + "' is not defined")
	}
	if conf.OnRename != "" && conf.LookupAction(conf.OnRename) == nil {
		return errors.New("Action '" + conf.OnRename + "' is not defined")
	}
	if conf.OnChmod != "" && conf.LookupAction(conf.OnChmod) == nil {
		return errors.New("Action '" + conf.OnChmod + "' is not defined")
	}

	return nil
}

func (conf *Config) LookupAction(actionName string) *Action {
	if actionName == "" {
		return nil
	}
	for _, action := range conf.Action {
		if action.Name == actionName {
			return &action
		}
	}
	return nil
}

func validateActionConfig(action *Action) error {
	if action.Name == "" {
		return errors.New("action's 'name' is empty")
	}

	if action.Interval == "" {
		action.Interval = "0"
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
