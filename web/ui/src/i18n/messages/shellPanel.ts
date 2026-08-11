// ShellPanel.vue: the terminal panel's chrome, plus the line written into the
// terminal itself when the shell exits.
export default {
  namespace: "shellPanel",
  messages: {
    "zh-CN": {
      workspace: "当前会话工作区：{ws}",
      toStacked: "切换为上下布局",
      toSideBySide: "切换为左右布局",
      noSession: "先开始一个会话，shell 会在它的工作区里打开",
      exited: "shell 已退出（code {code}），按任意键重启",
    },
    en: {
      workspace: "Current session workspace: {ws}",
      toStacked: "Switch to stacked layout",
      toSideBySide: "Switch to side-by-side layout",
      noSession: "Start a session first; the shell will open in its workspace",
      exited: "shell exited (code {code}); press any key to restart",
    },
    ja: {
      workspace: "現在のセッションのワークスペース：{ws}",
      toStacked: "上下レイアウトに切り替え",
      toSideBySide: "左右レイアウトに切り替え",
      noSession: "先にセッションを開始してください。shell はそのワークスペースで開きます",
      exited: "shell は終了しました（code {code}）。任意のキーで再起動します",
    },
    ko: {
      workspace: "현재 세션 작업 공간: {ws}",
      toStacked: "상하 레이아웃으로 전환",
      toSideBySide: "좌우 레이아웃으로 전환",
      noSession: "먼저 세션을 시작하세요. shell이 해당 작업 공간에서 열립니다",
      exited: "shell이 종료되었습니다(code {code}). 아무 키나 누르면 다시 시작합니다",
    },
  },
};
