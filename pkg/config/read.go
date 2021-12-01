package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rancher/os2/pkg/dmidecode"
	"github.com/rancher/rancherd/pkg/tpm"
	values "github.com/rancher/wrangler/pkg/data"
	"github.com/rancher/wrangler/pkg/data/convert"
	schemas2 "github.com/rancher/wrangler/pkg/schemas"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var (
	defaultMappers = schemas2.Mappers{
		NewToMap(),
		NewToSlice(),
		NewToBool(),
		&FuzzyNames{},
	}
	schemas = schemas2.EmptySchemas().Init(func(s *schemas2.Schemas) *schemas2.Schemas {
		s.AddMapper("config", defaultMappers)
		s.AddMapper("rancherOS", defaultMappers)
		s.AddMapper("install", defaultMappers)
		return s
	}).MustImport(Config{})
	schema = schemas.Schema("config")
)

func ToEnv(cfg Config) ([]string, error) {
	data, err := convert.EncodeToMap(&cfg)
	if err != nil {
		return nil, err
	}

	return mapToEnv("", data), nil
}

func mapToEnv(prefix string, data map[string]interface{}) []string {
	var result []string
	for k, v := range data {
		keyName := strings.ToUpper(prefix + convert.ToYAMLKey(k))
		keyName = strings.ReplaceAll(keyName, "RANCHEROS_", "COS_")
		if data, ok := v.(map[string]interface{}); ok {
			subResult := mapToEnv(keyName+"_", data)
			result = append(result, subResult...)
		} else {
			result = append(result, fmt.Sprintf("%s=%v", keyName, v))
		}
	}
	return result
}

func readFileFunc(path string) func() (map[string]interface{}, error) {
	return func() (map[string]interface{}, error) {
		return readFile(path)
	}
}

func readNested(data map[string]interface{}, overlay bool) (map[string]interface{}, error) {
	var (
		nestedConfigFiles = convert.ToStringSlice(values.GetValueN(data, "rancheros", "install", "configUrl"))
		funcs             []reader
	)

	if overlay {
		funcs = append(funcs, func() (map[string]interface{}, error) {
			return data, nil
		})
	}

	for _, nestedConfigFile := range nestedConfigFiles {
		funcs = append(funcs, readFileFunc(nestedConfigFile))
	}

	if !overlay {
		funcs = append(funcs, func() (map[string]interface{}, error) {
			return data, nil
		})
	}

	return merge(funcs...)
}

func readFile(path string) (result map[string]interface{}, _ error) {
	result = map[string]interface{}{}

	switch {
	case strings.HasPrefix(path, "http://"):
		fallthrough
	case strings.HasPrefix(path, "https://"):
		resp, err := http.Get(path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		buffer, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		return result, yaml.Unmarshal(buffer, &result)
	case strings.HasPrefix(path, "tftp://"):
		return tftpGet(path)
	}

	f, err := ioutil.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	data := map[string]interface{}{}
	if err := yaml.Unmarshal(f, &data); err != nil {
		return nil, err
	}

	return readNested(data, false)
}

type reader func() (map[string]interface{}, error)

func merge(readers ...reader) (map[string]interface{}, error) {
	d := map[string]interface{}{}
	for _, r := range readers {
		newData, err := r()
		if err != nil {
			return nil, err
		}
		if err := schema.Mapper.ToInternal(newData); err != nil {
			return nil, err
		}
		d = values.MergeMapsConcatSlice(d, newData)
	}
	return d, nil
}

func readConfigMap(cfg string, includeCmdline bool) (map[string]interface{}, error) {
	var (
		data map[string]interface{}
		err  error
	)

	if includeCmdline {
		data, err = merge(readCmdline, readFileFunc(cfg))
		if err != nil {
			return nil, err
		}
	} else {
		data, err = merge(readFileFunc(cfg))
		if err != nil {
			return nil, err
		}
	}

	if cfg != "" {
		values.PutValue(data, cfg, "rancheros", "install", "configUrl")
	}

	registrationURL := convert.ToString(values.GetValueN(data, "rancheros", "install", "registrationUrl"))
	registrationCA := convert.ToString(values.GetValueN(data, "rancheros", "install", "registrationCaCert"))
	if registrationURL != "" {
		isoURL := convert.ToString(values.GetValueN(data, "rancheros", "install", "isoUrl"))
		for {
			newData, err := returnRegistrationData(registrationURL, registrationCA)
			if err == nil {
				newISOURL := convert.ToString(values.GetValueN(newData, "rancheros", "install", "isoUrl"))
				if newISOURL == "" {
					if isoURL == "" {
						return nil, fmt.Errorf("rancheros.install.iso_url is required to be set in /proc/cmdline or MachineRegistration")
					}
					values.PutValue(newData, isoURL, "rancheros", "install", "isoUrl")
				}
				return newData, nil
			}
			logrus.Errorf("failed to read registration URL %s, retrying: %v", registrationURL, err)
			time.Sleep(15 * time.Second)
		}
	}

	return data, nil
}

func ToFile(cfg Config, output string) error {
	data, err := ToBytes(cfg)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(output, data, 0600)
}

func ToBytes(cfg Config) ([]byte, error) {
	var (
		data map[string]interface{}
		err  error
	)
	if len(cfg.Data) > 0 {
		data = values.MergeMaps(nil, cfg.Data)
	} else {
		data, err = convert.EncodeToMap(cfg)
		if err != nil {
			return nil, err
		}
	}
	values.RemoveValue(data, "install")
	values.RemoveValue(data, "rancheros", "install")
	bytes, err := yaml.Marshal(data)
	if err != nil {
		return nil, err
	}

	return append([]byte("#cloud-config\n"), bytes...), nil
}

func ReadConfig(cfg string, includeCmdline bool) (result Config, err error) {
	data, err := readConfigMap(cfg, includeCmdline)
	if err != nil {
		return result, err
	}

	if err := convert.ToObj(data, &result); err != nil {
		return result, err
	}

	result.Data = data
	return result, nil
}

func returnRegistrationData(url, ca string) (map[string]interface{}, error) {
	smbios, err := getSMBiosHeaders()
	if err != nil {
		return nil, err
	}
	data, err := tpm.Get([]byte(ca), url, smbios)
	if err != nil {
		return nil, err
	}
	logrus.Infof("Retrieved config from registrationURL: %s", data)
	result := map[string]interface{}{}
	return result, json.Unmarshal(data, &result)
}

func getSMBiosHeaders() (http.Header, error) {
	smbios, err := dmidecode.Decode()
	if err != nil {
		return nil, err
	}
	smbiosData, err := json.Marshal(smbios)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("X-Cattle-Smbios", base64.StdEncoding.EncodeToString(smbiosData))
	return header, nil
}

func readCmdline() (map[string]interface{}, error) {
	//supporting regex https://regexr.com/4mq0s
	parser, err := regexp.Compile(`(\"[^\"]+\")|([^\s]+=(\"[^\"]+\")|([^\s]+))`)
	if err != nil {
		return nil, nil
	}

	procCmdLine := os.Getenv("PROC_CMDLINE")
	if procCmdLine == "" {
		procCmdLine = "/proc/cmdline"
	}
	bytes, err := ioutil.ReadFile(procCmdLine)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	data := map[string]interface{}{}
	for _, item := range parser.FindAllString(string(bytes), -1) {
		parts := strings.SplitN(item, "=", 2)
		value := "true"
		if len(parts) > 1 {
			value = strings.Trim(parts[1], `"`)
		}
		keys := strings.Split(strings.Trim(parts[0], `"`), ".")
		existing, ok := values.GetValue(data, keys...)
		if ok {
			switch v := existing.(type) {
			case string:
				values.PutValue(data, []string{v, value}, keys...)
			case []string:
				values.PutValue(data, append(v, value), keys...)
			}
		} else {
			values.PutValue(data, value, keys...)
		}
	}

	if err := schema.Mapper.ToInternal(data); err != nil {
		return nil, err
	}

	return readNested(data, true)
}
