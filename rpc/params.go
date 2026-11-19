package rpc

import "errors"
import "fmt"

func GetParameter(params map[string]any, key string) (any, error) {
	param, hasParam := params[key]
	if !hasParam {
		return nil, errors.New(fmt.Sprintf("Missing parameter '%s'.", key))
	}
	return param, nil
}

func GetBooleanParameter(params map[string]any, key string) (bool, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		return false, err
	}
	paramBoolean, paramIsBoolean := param.(bool)
	if !paramIsBoolean {
		return false, errors.New(fmt.Sprintf("Parameter '%s' not of type boolean.", key))
	}
	return paramBoolean, nil
}

func GetStringParameter(params map[string]any, key string) (string, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		return "", err
	}
	paramString, paramIsString := param.(string)
	if !paramIsString {
		return "", errors.New(fmt.Sprintf("Parameter '%s' not of type string.", key))
	}
	return paramString, nil
}
