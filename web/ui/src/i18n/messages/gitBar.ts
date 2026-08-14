// Copy for src/components/GitBar.vue — the version control line under the file
// panel header. "No repository" is phrased as a fact plus its consequence, not as
// an error: working without git is a choice, but working without knowing you are
// is not.
export default {
  namespace: "gitBar",
  messages: {
    "zh-CN": {
      noRepo: "未纳入 git 版本控制",
      noRepoHint: "这个工作区不是 git 仓库。撤回（会话内的 checkpoint）是唯一的回退手段，没有可对比的提交历史。",
      unavailable: "git 状态不可用：{reason}",
      detached: "HEAD 游离",
      unknownBranch: "未知分支",
      noCommits: "尚无提交",
      clean: "无未提交改动",
      uncommitted: "{n} 项未提交",
      root: "仓库根目录：{root}",
      upstream: "上游：{upstream}",
      breakdown: "已暂存 {staged} · 未暂存 {unstaged} · 未跟踪 {untracked} · 冲突 {conflicted}",
    },
    en: {
      noRepo: "not under version control",
      noRepoHint:
        "This workspace is not a git repository. Rewind (the session's checkpoints) is the only way back, and there is no committed history to compare against.",
      unavailable: "git status unavailable: {reason}",
      detached: "detached HEAD",
      unknownBranch: "unknown branch",
      noCommits: "no commits yet",
      clean: "nothing uncommitted",
      uncommitted: "{n} uncommitted",
      root: "Repository root: {root}",
      upstream: "Upstream: {upstream}",
      breakdown: "{staged} staged · {unstaged} unstaged · {untracked} untracked · {conflicted} conflicted",
    },
    ja: {
      noRepo: "バージョン管理外",
      noRepoHint:
        "このワークスペースは git リポジトリではありません。巻き戻し（セッションのチェックポイント）が唯一の復旧手段で、比較できるコミット履歴はありません。",
      unavailable: "git の状態を取得できません: {reason}",
      detached: "detached HEAD",
      unknownBranch: "不明なブランチ",
      noCommits: "コミットなし",
      clean: "未コミットなし",
      uncommitted: "未コミット {n} 件",
      root: "リポジトリのルート: {root}",
      upstream: "アップストリーム: {upstream}",
      breakdown: "ステージ済み {staged} · 未ステージ {unstaged} · 未追跡 {untracked} · 衝突 {conflicted}",
    },
    ko: {
      noRepo: "버전 관리 대상 아님",
      noRepoHint:
        "이 워크스페이스는 git 저장소가 아닙니다. 되돌리기(세션 체크포인트)가 유일한 복구 수단이며, 비교할 커밋 이력이 없습니다.",
      unavailable: "git 상태를 확인할 수 없음: {reason}",
      detached: "detached HEAD",
      unknownBranch: "알 수 없는 브랜치",
      noCommits: "커밋 없음",
      clean: "커밋되지 않은 변경 없음",
      uncommitted: "커밋되지 않음 {n}개",
      root: "저장소 루트: {root}",
      upstream: "업스트림: {upstream}",
      breakdown: "스테이지 {staged} · 미스테이지 {unstaged} · 미추적 {untracked} · 충돌 {conflicted}",
    },
  },
};
