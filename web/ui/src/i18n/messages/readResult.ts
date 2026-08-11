export default {
  namespace: "readResult",
  messages: {
    "zh-CN": {
      lines: "{n} 行",
      truncated: "只显示 {first}–{last}（{by}）",
      byBytes: "字节上限",
      byLines: "行数上限",
      suggestContinue: "继续读 {path}，从第 {line} 行开始",
      continueFrom: "继续读第 {line} 行往后",
    },
    en: {
      lines: "{n} lines",
      truncated: "Showing {first}–{last} only ({by})",
      byBytes: "byte limit",
      byLines: "line limit",
      suggestContinue: "Continue reading {path} from line {line}",
      continueFrom: "Continue from line {line}",
    },
    ja: {
      lines: "{n} 行",
      truncated: "{first}–{last} 行のみ表示（{by}）",
      byBytes: "バイト上限",
      byLines: "行数上限",
      suggestContinue: "{path} の続きを {line} 行目から読んでください",
      continueFrom: "{line} 行目以降を読み続ける",
    },
    ko: {
      lines: "{n}줄",
      truncated: "{first}–{last}줄만 표시({by})",
      byBytes: "바이트 상한",
      byLines: "줄 수 상한",
      suggestContinue: "{path}를 {line}번째 줄부터 이어서 읽어 주세요",
      continueFrom: "{line}번째 줄부터 계속 읽기",
    },
  },
};
