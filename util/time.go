package util

import "time"

// GetSecondTime 返回当前 Unix 时间戳（秒）。
func GetSecondTime() int64 {
	return time.Now().Unix()
}
func GetMilliTime() int64 {
	return time.Now().UnixMilli()
}
