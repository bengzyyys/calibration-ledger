package calibrate

import "time"

// parseInstant 把带偏移的 RFC3339 时间（如申请时间、计划建立时间，可带
// 不同偏移或 Z）解析为实际时刻。不同文字形式（如 Z 与 +00:00）只要表示
// 同一绝对时刻，解析结果即相同；无法识别时 ok 为 false。
//
// 这是全部使用申请记录与校准计划历史共用的时间识别规则，两处历史不在
// 各自维护解析方式。
func parseInstant(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// compareHistoricalInstant 是全部使用申请记录与校准计划历史共用的时间
// 先后规则，两处查询只在这里维护“时间能否识别、实际时刻比较、异常时间
// 如何排列”：
//
//   - 两条时间都能识别时换算为实际时刻比较：Before 以绝对时刻为准，与
//     记录保存时自带的偏移、时间文字形式（含 Z 与 +00:00 之别）及查询
//     当天的系统时区无关，跨日或偏移不同也按实际时刻先后；
//   - 恰好一条无法识别时，可识别记录排在无法识别记录之前，顺序仍确定，
//     正常由本台账保存的 RFC3339 记录不会走到无法识别的分支；
//   - 两条都无法识别时按保存的时间原文字升序，保证结果确定。
//
// 返回负数表示 a 应排在 b 前，正数表示应排在后面，0 表示共同规则下并列：
// 两条可识别时间表示同一实际时刻（Z、+00:00 或其他偏移只要时刻相同），
// 或两条无法识别时间的原文字相同。并列时的最终次序由调用方各自的同刻
// 规则决定（使用申请再按器具编号升序，计划直接保留台账原有次序），并由
// 调用方配合稳定排序保留每条记录、不合并。
func compareHistoricalInstant(a, b string) int {
	ta, oka := parseInstant(a)
	tb, okb := parseInstant(b)
	switch {
	case oka && okb:
		// 按实际时刻比较：与记录保存时所带的偏移及文字形式无关。
		switch {
		case ta.Before(tb):
			return -1
		case tb.Before(ta):
			return 1
		default:
			return 0
		}
	case oka != okb:
		// 无法识别的异常记录排到可解析记录之后。
		if oka {
			return -1
		}
		return 1
	default:
		// 两条都无法解析时退回按原文字比较；文字相同交由调用方的同刻规则。
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
}
