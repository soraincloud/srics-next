import type { Item } from "./api";
export type Chapter = {
  id: string;
  novelId: string;
  title: string;
  body?: string;
  position: number;
  revision: number;
  deleted: string;
  updated: string;
};
export type Novel = {
  item: Item;
  chapters: Chapter[];
  trash: Chapter[];
  reading: string;
  bookmark?: NovelBookmark;
};
export type NovelBookmark = {
  chapter: string;
  paragraph: number;
  fraction: number;
  revision: number;
  updated?: string;
};
export type ChapterVersion = {
  revision: number;
  title: string;
  body?: string;
  saved: string;
};
export const newID = () => crypto.randomUUID().replaceAll("-", "");
export const parseTags = (text: string) =>
  text
    .split(/[,，]/)
    .map((t) => t.trim())
    .filter(Boolean);
