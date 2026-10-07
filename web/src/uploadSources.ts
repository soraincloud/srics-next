export type SourceTask = {
  module: string;
  autoName?: boolean;
  files: { name: string; size: number }[];
};

export function matchUploadSources(task: SourceTask, files: File[]): File[] {
  if (task.autoName && task.module === "images") {
    if (task.files.length !== 1 || files.length !== 1 || files[0]!.size !== task.files[0]!.size)
      throw new Error("请重新选择此任务原来的图片；文件大小需要一致，传输时会核对内容。");
    return files;
  }
  const mapped = task.files.map((f) => files.find((v) => v.name === f.name && v.size === f.size));
  if (mapped.some((v) => !v) || files.length !== task.files.length)
    throw new Error("请重新选择这个任务原来的文件，名称和数量需要一致。");
  return mapped as File[];
}
