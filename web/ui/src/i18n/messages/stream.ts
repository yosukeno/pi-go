// Connection and stream error copy produced by agent/useAgentStream.ts. These
// end up in streamError / outage.message, which the unreachable page renders.
export default {
  namespace: "stream",
  messages: {
    "zh-CN": {
      unauthorized: "未授权：URL 里的 token 不对",
      connectFailed: "连接失败：HTTP {status}",
      interrupted: "连接中断",
      disconnected: "连接已断开",
      unknownError: "unknown error",
    },
    en: {
      unauthorized: "Unauthorized: the token in the URL is wrong",
      connectFailed: "Connection failed: HTTP {status}",
      interrupted: "Connection interrupted",
      disconnected: "Connection lost",
      unknownError: "unknown error",
    },
    ja: {
      unauthorized: "未認証：URL のトークンが正しくありません",
      connectFailed: "接続に失敗しました：HTTP {status}",
      interrupted: "接続が中断されました",
      disconnected: "接続が切断されました",
      unknownError: "不明なエラー",
    },
    ko: {
      unauthorized: "인증되지 않음: URL의 토큰이 올바르지 않습니다",
      connectFailed: "연결 실패: HTTP {status}",
      interrupted: "연결이 중단되었습니다",
      disconnected: "연결이 끊어졌습니다",
      unknownError: "알 수 없는 오류",
    },
  },
};
