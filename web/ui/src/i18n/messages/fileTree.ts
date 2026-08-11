// Copy for src/components/FileTree.vue — column headers and the loading /
// error / empty hints of the workspace file tree.
export default {
  namespace: "fileTree",
  messages: {
    "zh-CN": {
      loadFailed: "加载失败，点击重试",
      colName: "名称",
      colSize: "大小",
      colTime: "时间",
      emptyDir: "空目录",
    },
    en: {
      loadFailed: "Failed to load, click to retry",
      colName: "Name",
      colSize: "Size",
      colTime: "Modified",
      emptyDir: "Empty directory",
    },
    ja: {
      loadFailed: "読み込みに失敗しました。クリックで再試行",
      colName: "名前",
      colSize: "サイズ",
      colTime: "更新日時",
      emptyDir: "空のディレクトリ",
    },
    ko: {
      loadFailed: "로드 실패, 클릭하여 다시 시도",
      colName: "이름",
      colSize: "크기",
      colTime: "수정 시간",
      emptyDir: "빈 디렉터리",
    },
  },
};
