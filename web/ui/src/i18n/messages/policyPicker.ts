export default {
  namespace: "policyPicker",
  messages: {
    "zh-CN": {
      tip: "审批模式：哪些操作需要你点头",
      menuTitle: "应如何批准 pi-go 的操作？",
      modes: {
        strict: { label: "请求批准", desc: "编辑文件和运行命令前都要你点头" },
        standard: { label: "替我审批", desc: "默认：只有 bash 命令需要你点头" },
        auto: { label: "完全访问", desc: "所有操作都不再询问，直接执行" },
      },
    },
    en: {
      tip: "Approval mode: which actions need your approval",
      menuTitle: "How should pi-go actions be approved?",
      modes: {
        strict: { label: "Ask for Approval", desc: "You approve every file edit and command before it runs" },
        standard: { label: "Approve for Me", desc: "Default: only bash commands need your approval" },
        auto: { label: "Full Access", desc: "Nothing is asked anymore; everything runs directly" },
      },
    },
    ja: {
      tip: "承認モード：どの操作に承認が必要か",
      menuTitle: "pi-go の操作をどう承認しますか？",
      modes: {
        strict: { label: "承認を求める", desc: "ファイルの編集とコマンドの実行前に毎回承認が必要です" },
        standard: { label: "代理で承認", desc: "デフォルト：bash コマンドのみ承認が必要です" },
        auto: { label: "フルアクセス", desc: "すべての操作を確認せず、そのまま実行します" },
      },
    },
    ko: {
      tip: "승인 모드: 어떤 작업에 승인이 필요한지",
      menuTitle: "pi-go 작업을 어떻게 승인할까요?",
      modes: {
        strict: { label: "승인 요청", desc: "파일 편집과 명령 실행 전에 모두 승인이 필요합니다" },
        standard: { label: "대신 승인", desc: "기본값: bash 명령만 승인이 필요합니다" },
        auto: { label: "전체 접근", desc: "모든 작업을 묻지 않고 바로 실행합니다" },
      },
    },
  },
};
