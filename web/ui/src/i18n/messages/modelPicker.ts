export default {
  namespace: "modelPicker",
  messages: {
    "zh-CN": {
      tip: "切换模型，对话历史原样保留",
      tipDisabled: "运行中不能切换模型",
      selectModel: "选择模型",
      needsKey: "需要 {key}",
      subagentLabel: "子 agent: {model}",
      subagentTip: "只读（explore）subagent 会跑 {model} 而不是 {current}；在 ~/.pi-go/providers.json 里配置",
    },
    en: {
      tip: "Switch model — conversation history is kept as-is",
      tipDisabled: "Cannot switch models while a run is in flight",
      selectModel: "Select Model",
      needsKey: "needs {key}",
      subagentLabel: "sub-agent: {model}",
      subagentTip: "Read-only (explore) subagents run {model} instead of {current}; configure it in ~/.pi-go/providers.json",
    },
    ja: {
      tip: "モデルを切り替え。会話履歴はそのまま保持されます",
      tipDisabled: "実行中はモデルを切り替えられません",
      selectModel: "モデルを選択",
      needsKey: "{key} が必要",
      subagentLabel: "サブ agent: {model}",
      subagentTip: "読み取り専用（explore）subagent は {current} ではなく {model} で実行されます。~/.pi-go/providers.json で設定してください",
    },
    ko: {
      tip: "모델 전환 — 대화 기록은 그대로 유지됩니다",
      tipDisabled: "실행 중에는 모델을 전환할 수 없습니다",
      selectModel: "모델 선택",
      needsKey: "{key} 필요",
      subagentLabel: "서브 agent: {model}",
      subagentTip: "읽기 전용(explore) subagent는 {current} 대신 {model}로 실행됩니다. ~/.pi-go/providers.json에서 설정하세요",
    },
  },
};
