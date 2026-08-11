export default {
  namespace: "usageMeter",
  messages: {
    "zh-CN": {
      hint: "本会话累计 token：输入 {input}（缓存命中 {cachePct}%），输出 {output} — 点击查看明细",
      totalLabel: "本会话累计 tok",
      cacheTag: "缓存命中 {pct}%",
      rows: {
        inputTotal: "输入总计",
        cacheHit: "其中缓存命中",
        billedInput: "实付输入",
        output: "输出",
        reasoning: "其中思考",
      },
      foot: "缓存命中是输入的一部分，不是额外增加；每轮都会重发整段对话，所以累计量比对话本身增长得快。",
    },
    en: {
      hint: "Session total tokens: input {input} (cache hit {cachePct}%), output {output} — click for details",
      totalLabel: "session total tok",
      cacheTag: "cache hit {pct}%",
      rows: {
        inputTotal: "Total input",
        cacheHit: "of which cached",
        billedInput: "Billed input",
        output: "Output",
        reasoning: "of which thinking",
      },
      foot: "Cache hits are part of the input, not an addition; the whole conversation is resent every turn, so the total grows faster than the conversation itself.",
    },
    ja: {
      hint: "このセッションの累計 token：入力 {input}（キャッシュヒット {cachePct}%）、出力 {output} — クリックで明細を表示",
      totalLabel: "このセッションの累計 tok",
      cacheTag: "キャッシュヒット {pct}%",
      rows: {
        inputTotal: "入力合計",
        cacheHit: "うちキャッシュヒット",
        billedInput: "実質入力",
        output: "出力",
        reasoning: "うち思考",
      },
      foot: "キャッシュヒットは入力の一部であり、追加ではありません。毎ターン会話全体が再送されるため、累計は会話自体より速く増えます。",
    },
    ko: {
      hint: "이 세션의 누적 token: 입력 {input}(캐시 적중 {cachePct}%), 출력 {output} — 클릭하여 상세 보기",
      totalLabel: "이 세션의 누적 tok",
      cacheTag: "캐시 적중 {pct}%",
      rows: {
        inputTotal: "입력 합계",
        cacheHit: "그중 캐시 적중",
        billedInput: "실제 과금 입력",
        output: "출력",
        reasoning: "그중 사고",
      },
      foot: "캐시 적중은 입력의 일부이며 추가되는 것이 아닙니다. 매 턴 전체 대화가 다시 전송되므로 누적량이 대화 자체보다 빠르게 늘어납니다.",
    },
  },
};
