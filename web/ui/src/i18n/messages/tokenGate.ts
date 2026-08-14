// TokenGate.vue: the page that takes over the viewport when the API answers
// 401 — token absent or rejected.
export default {
  namespace: "tokenGate",
  messages: {
    "zh-CN": {
      title: "缺少访问令牌",
      subtitle: "这个实例开启了令牌校验，页面需要 token 才能调用 API。在下面粘贴令牌即可进入。",
      rejected: "刚才带的令牌被服务器拒绝了，请换成本次启动打印的那个。",
      hintStartup: "令牌在服务启动时打印的地址里，即 {url} 后面的部分",
      hintDocker: "容器部署时把地址换成宿主机端口，例如 {url2}",
      placeholder: "粘贴令牌",
      enter: "进入",
    },
    en: {
      title: "Access token required",
      subtitle: "This instance checks a shared token; the page needs one before the API will answer. Paste it below to continue.",
      rejected: "The token presented was rejected by the server — use the one printed by this launch.",
      hintStartup: "The token is in the address printed at startup, after {url}",
      hintDocker: "Behind Docker, swap in the host port, e.g. {url2}",
      placeholder: "Paste the token",
      enter: "Enter",
    },
    ja: {
      title: "アクセストークンが必要です",
      subtitle: "このインスタンスはトークン検証が有効です。API を利用するにはトークンが必要です。下に貼り付けてください。",
      rejected: "提示されたトークンはサーバーに拒否されました。今回の起動時に表示されたものを使用してください。",
      hintStartup: "トークンは起動時に表示されるアドレスの {url} 以降の部分です",
      hintDocker: "Docker ではホスト側ポートに置き換えてください（例: {url2}）",
      placeholder: "トークンを貼り付け",
      enter: "進む",
    },
    ko: {
      title: "액세스 토큰이 필요합니다",
      subtitle: "이 인스턴스는 토큰 검증이 켜져 있어 API를 호출하려면 토큰이 필요합니다. 아래에 붙여넣으세요.",
      rejected: "제시된 토큰이 서버에서 거부되었습니다. 이번 시작 시 출력된 토큰을 사용하세요.",
      hintStartup: "토큰은 시작 시 출력되는 주소의 {url} 뒷부분에 있습니다",
      hintDocker: "Docker 에서는 호스트 포트로 바꾸세요 (예: {url2})",
      placeholder: "토큰 붙여넣기",
      enter: "들어가기",
    },
  },
};
