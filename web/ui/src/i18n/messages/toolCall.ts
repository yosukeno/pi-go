// Tool call row: status badges and the soft correction hint.
export default {
  namespace: "toolCall",
  messages: {
    "zh-CN": {
      running: "执行中",
      waitingApproval: "等待批准",
      orphaned: "未完成",
      failed: "失败",
      correctsHint: "看起来是在纠正前面那次失败的调用",
    },
    en: {
      running: "Running",
      waitingApproval: "Waiting for approval",
      orphaned: "Incomplete",
      failed: "Failed",
      correctsHint: "Looks like a correction of the earlier failed call",
    },
    ja: {
      running: "実行中",
      waitingApproval: "承認待ち",
      orphaned: "未完了",
      failed: "失敗",
      correctsHint: "前の失敗した呼び出しを修正しているようです",
    },
    ko: {
      running: "실행 중",
      waitingApproval: "승인 대기 중",
      orphaned: "미완료",
      failed: "실패",
      correctsHint: "이전에 실패한 호출을 수정하는 것 같습니다",
    },
  },
};
