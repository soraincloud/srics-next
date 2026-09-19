export const modules = [
  {
    id: "comics", name: "漫画", icon: "book", color: "orange", stage: "M2",
    sub: "文件夹导入，按页阅读和收藏。", kind: "名称 · 标签 · 整本下载",
    features: ["文件夹上传，按数字排列页序", "自定义名称、标签与组合搜索", "首页预览与连续阅读", "无损 WebP 转换、整本 ZIP 下载"],
  },
  {
    id: "novels", name: "小说", icon: "text", color: "amber", stage: "M3",
    sub: "管理章节，随时阅读和编辑。", kind: "章节 · 标签 · 阅读",
    features: ["自定义名称、标签与组合搜索", "章节新增、编辑、删除和排序", "自动保存、冲突检查与修订记录", "独立阅读模式、TXT 导出"],
  },
  {
    id: "images", name: "图片", icon: "image", color: "green", stage: "M2",
    sub: "保存喜欢的图片，随机发现灵感。", kind: "列表 · 随机浏览",
    features: ["批量上传与原件下载", "列表浏览与随机照片墙", "同轮不重复的独立随机池", "无需名称、标签或搜索"],
  },
  {
    id: "photos", name: "个人照片", icon: "camera", color: "blue", stage: "M3",
    sub: "保留照片原件，备份生活片段。", kind: "原件 · 照片备份",
    features: ["保留原始字节与照片元数据", "批量上传、简单列表浏览", "分别显示上传状态与备份状态", "原件取回、回收站恢复"],
  },
  {
    id: "private", name: "私密照片", icon: "lock", color: "purple", stage: "M4",
    sub: "独立解锁，浏览私密的回忆。", kind: "加密 · 随机浏览",
    features: ["原件、名称和预览加密保存", "解锁后列表浏览与随机浏览", "各设备独立解锁、闲置自动锁定", "加密备份与原件取回"],
  },
  {
    id: "files", name: "个人文件", icon: "folder", color: "indigo", stage: "M4",
    sub: "加密保存重要文件与敏感资料。", kind: "加密 · 名称搜索",
    features: ["整个模块默认加密", "设置名称、按名称搜索", "批量上传与原件下载", "删除、回收站与恢复"],
  },
];

export const stages = [
  { id: "M0", name: "工程与恢复验证", detail: "无损转换、加密存储与备份恢复", state: "已完成" },
  { id: "M1", name: "公共基础", detail: "登录、上传任务、回收站与备份状态", state: "已接入" },
  { id: "M2", name: "漫画与图片", detail: "文件夹导入、漫画阅读与随机照片墙", state: "当前版本" },
  { id: "M3", name: "小说与个人照片", detail: "个人照片已开放；章节编辑、修订与阅读待开发", state: "部分完成" },
  { id: "M4", name: "私密内容", detail: "独立解锁、加密名称与受保护的预览", state: "待开发" },
  { id: "M5", name: "发布与恢复验收", detail: "简单安装、云端备份与完整恢复验收", state: "待开发" },
];
