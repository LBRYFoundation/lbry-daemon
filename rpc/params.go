package rpc

import "errors"
import "fmt"

func HasParameter(params map[string]any, key string) bool {
	_, hasParam := params[key]
	return hasParam
}

func GetParameter(params map[string]any, key string) (any, error) {
	param, hasParam := params[key]
	if !hasParam {
		return nil, errors.New(fmt.Sprintf("Missing parameter '%s'.", key))
	}
	return param, nil
}

func GetBooleanParameter(params map[string]any, key string, failOnMissing bool, defaultValue bool) (bool, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		if failOnMissing {
			return defaultValue, err
		}
		return defaultValue, nil
	}
	paramBoolean, paramIsBoolean := param.(bool)
	if !paramIsBoolean {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' not of type boolean.", key))
	}
	return paramBoolean, nil
}

func GetStringParameter(params map[string]any, key string, failOnMissing bool, defaultValue string) (string, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		if failOnMissing {
			return defaultValue, err
		}
		return defaultValue, nil
	}
	paramString, paramIsString := param.(string)
	if !paramIsString {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' not of type string.", key))
	}
	return paramString, nil
}
