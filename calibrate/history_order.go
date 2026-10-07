package calibrate

import (
	"sort"
	"time"
)

// parseInstant 把带偏移的 RFC3339 时间（如申请时间、计划建立时间，可带
// 不同偏移或 Z）解析为实际时刻。不同文字形式（如 Z 与 +00:00）只要表示
// 同一绝对时刻，解析结果即相同；无法按 RFC3339 识别时 ok 为 false。
func parseInstant(s string) (t time.Time, ok bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// historyTimeKey 是一条历史记录参与“时间先后”排列时所需的键：保存的时间
// 原文字、解析出的实际时刻，以及该时间能否被识别。
type historyTimeKey struct {
	raw     string
	instant time.Time
	ok      bool
}

func parseHistoryTimeKey(raw string) historyTimeKey {
	t, ok := parseInstant(raw)
	return historyTimeKey{raw: raw, instant: t, ok: ok}
}

// compareHistoryTime 比较两条历史记录的时间先后。这是全部使用申请记录与
// 按器具查询/核对校准计划两处共同维护、必须一致的规则：
//
//   - 两者的时间都能识别时，按实际时刻从早到晚排列，与记录保存时所带的
//     时区偏移及文字形式无关（文字里日期、小时更大的记录未必更晚，跨日或
//     偏移不同也按实际时刻）；Z、+00:00 或其他偏移只要表示同一实际时刻即
//     视为同刻，返回 0，交由调用方套用各自记录的同刻次序。查询当天本机
//     所处的时区不参与比较，不能改变历史顺序。
//   - 只有一方时间无法识别时，可识别的记录在前，无法识别的异常记录统一
//     排在有效时间记录之后。
//   - 两者时间都无法识别时，按保存的时间原文字升序；原文字也相同返回 0，
//     同样交由调用方的同刻次序处理，保证无法识别的记录之间顺序仍确定。
//
// 返回负数表示 a 应排在 b 之前，正数表示在后，0 表示仅凭时间无法区分先后。
func compareHistoryTime(a, b historyTimeKey) int {
	switch {
	case a.ok && b.ok:
		// Before/After 以绝对时刻为准，与偏移及文字形式无关；同刻返回 0。
		if a.instant.Before(b.instant) {
			return -1
		}
		if b.instant.Before(a.instant) {
			return 1
		}
		return 0
	case a.ok != b.ok:
		// 无法识别时间的异常记录排到可解析记录之后。
		if a.ok {
			return -1
		}
		return 1
	default:
		// 两条都无法解析时按保存的时间原文字比较，文字相同再交同刻次序。
		if a.raw < b.raw {
			return -1
		}
		if a.raw > b.raw {
			return 1
		}
		return 0
	}
}

// sortHistoryByTime 按共同的时间先后规则（见 compareHistoryTime）稳定排列
// 历史记录。timeText 取出每条记录保存的时间原文字；tieBefore 在时间无法
// 区分先后（实际时刻相同，或两者时间都无法识别且原文字也相同）时给出该
// 类记录自己的同刻次序，返回 true 表示 a 应排在 b 之前，传 nil 表示同刻
// 一律保留入参切片中的原有次序。时间能区分先后时绝不调用 tieBefore，因此
// 各记录特有的次序规则不会干扰共同的实际时刻先后。
//
// 排序稳定：连 tieBefore 也无法区分先后的记录保留其原有次序，每条记录都
// 单独参与排列、不因时间相同而被合并。
func sortHistoryByTime[T any](items []T, timeText func(T) string, tieBefore func(a, b T) bool) {
	// 把记录与其时间键配对后一起排序：排列过程中每条记录的键必须随记录
	// 一起移动，键与记录的对应关系才不会在交换位置后错位。
	type entry struct {
		item T
		key  historyTimeKey
	}
	entries := make([]entry, len(items))
	for i, it := range items {
		entries[i] = entry{item: it, key: parseHistoryTimeKey(timeText(it))}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if c := compareHistoryTime(entries[i].key, entries[j].key); c != 0 {
			return c < 0
		}
		return tieBefore != nil && tieBefore(entries[i].item, entries[j].item)
	})
	for i := range entries {
		items[i] = entries[i].item
	}
}
