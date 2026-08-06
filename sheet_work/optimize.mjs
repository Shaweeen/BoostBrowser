import fs from "node:fs/promises";
import { FileBlob, SpreadsheetFile } from "@oai/artifact-tool";

const inputPath = "/Users/chanx/Desktop/BoostBrowser/成都项目备用金26年7月15日-转换.xlsx";
const outputDir = "/Users/chanx/Desktop/BoostBrowser/outputs/019f8038-0a12-70d3-9be9-7db031ea188c";
const workDir = "/Users/chanx/Desktop/BoostBrowser/sheet_work";
const outputPath = `${outputDir}/成都项目备用金-自动日期自动合计.xlsx`;

await fs.mkdir(outputDir, { recursive: true });
const input = await FileBlob.load(inputPath);
const workbook = await SpreadsheetFile.importXlsx(input);

const sheet = workbook.worksheets.getItem("备用金");
const opening = sheet.getRange("A3:H3").values[0];
const expenseRows = sheet.getRange("J3:P55").values;
const incomeRows = sheet.getRange("R3:W55").values;

function serialToDateCode(serial) {
  if (serial === null || serial === undefined || serial === "") return null;
  // 该笔位于 8 月 13/15 日记录之前，原输入“11/8/26”应为 2026-08-11。
  if (Number(serial) === 46334) return 811;
  const date = new Date((Number(serial) - 25569) * 86400 * 1000);
  const year = date.getUTCFullYear();
  const month = date.getUTCMonth() + 1;
  const day = date.getUTCDate();
  // 原表中 13/8/26、15/8/26 被 Excel 误识别为 2013/2015 年；还原为 2026-08-13/15。
  if (year < 2020) return month * 100 + (year % 100);
  return month * 100 + day;
}

const transactions = [];
let order = 0;
if (opening[4] !== null && opening[4] !== "") {
  transactions.push({
    dateCode: serialToDateCode(opening[1]),
    description: opening[2] || "备用金拨付",
    income: Number(opening[4]) || 0,
    expense: null,
    handler: opening[5] || "",
    note: [opening[3], opening[6] ? `收款人：${opening[6]}` : "", opening[7]].filter(Boolean).join("；"),
    order: order++,
  });
}

for (const row of expenseRows) {
  const amount = row[4];
  const description = row[2];
  if ((amount === null || amount === "") && !description) continue;
  transactions.push({
    dateCode: serialToDateCode(row[1]),
    description: description || "",
    income: null,
    expense: Number(amount) || 0,
    handler: row[5] || "",
    note: row[6] || "",
    order: order++,
  });
}

for (const row of incomeRows) {
  const amount = row[3];
  const description = row[1];
  if ((amount === null || amount === "") && !description) continue;
  transactions.push({
    dateCode: serialToDateCode(row[0]),
    description: description || "",
    income: Number(amount) || 0,
    expense: null,
    handler: row[4] || "",
    note: row[5] || "",
    order: order++,
  });
}

transactions.sort((a, b) => {
  const ak = a.dateCode ?? 99999;
  const bk = b.dateCode ?? 99999;
  return ak - bk || a.order - b.order;
});

for (const table of sheet.tables.items) table.delete();
sheet.deleteAllDrawings();
sheet.getRange("A1:W220").unmerge();
sheet.getRange("A1:W220").clear({ applyTo: "all" });
sheet.showGridLines = false;

sheet.getRange("A1:I1").merge();
sheet.getRange("A1").values = [["成都项目备用金流水"]];
sheet.getRange("A2").values = [["年份"]];
sheet.getRange("B2").values = [[2026]];
sheet.getRange("D2").values = [["累计收入"]];
sheet.getRange("E2").formulas = [["=SUM(E9:E208)"]];
sheet.getRange("F2").values = [["累计支出"]];
sheet.getRange("G2").formulas = [["=SUM(F9:F208)"]];
sheet.getRange("H2").values = [["当前余额"]];
sheet.getRange("I2").formulas = [["=ROUND(E2-G2,2)"]];

sheet.getRange("A4:I4").merge();
sheet.getRange("A4").values = [["日常录入：日期数字填 715 即自动显示 2026-07-15；金额填在“收入”或“支出”其中一列，合计和余额会自动更新。"]];
sheet.getRange("A6:I6").merge();
sheet.getRange("A6").values = [["黄色单元格为数字输入区；跨年时只需修改上方“年份”。用途、经办人、备注可按需填写。"]];

sheet.getRange("A8:I8").values = [[
  "序号", "日期数字", "日期（自动）", "用途说明", "收入（元）", "支出（元）", "经办人", "备注", "结余（自动）",
]];

const dataStart = 9;
const dataEnd = 208;
const dataRows = Array.from({ length: dataEnd - dataStart + 1 }, () => [null, null, null, null, null, null, null, null, null]);
transactions.forEach((t, i) => {
  dataRows[i][1] = t.dateCode;
  dataRows[i][3] = t.description;
  dataRows[i][4] = t.income;
  dataRows[i][5] = t.expense;
  dataRows[i][6] = t.handler;
  dataRows[i][7] = t.note;
});
sheet.getRange(`A${dataStart}:I${dataEnd}`).values = dataRows;

sheet.getRange(`A${dataStart}`).formulas = [[`=IF(OR(B${dataStart}<>"",D${dataStart}<>"",E${dataStart}<>"",F${dataStart}<>"",G${dataStart}<>"",H${dataStart}<>""),ROW()-8,"")`]];
sheet.getRange(`A${dataStart}:A${dataEnd}`).fillDown();
sheet.getRange(`C${dataStart}`).formulas = [[`=IF(B${dataStart}="","",DATE($B$2,INT(B${dataStart}/100),MOD(B${dataStart},100)))`]];
sheet.getRange(`C${dataStart}:C${dataEnd}`).fillDown();
sheet.getRange(`I${dataStart}`).formulas = [[`=IF(OR(B${dataStart}<>"",D${dataStart}<>"",E${dataStart}<>"",F${dataStart}<>"",G${dataStart}<>"",H${dataStart}<>""),ROUND(SUM($E$${dataStart}:E${dataStart})-SUM($F$${dataStart}:F${dataStart}),2),"")`]];
sheet.getRange(`I${dataStart}:I${dataEnd}`).fillDown();

sheet.getRange("A1:I1").format = {
  fill: "#0F4C5C",
  font: { bold: true, color: "#FFFFFF", size: 18 },
  horizontalAlignment: "center",
  verticalAlignment: "center",
};
sheet.getRange("A1:I1").format.rowHeight = 34;

sheet.getRange("A2:I2").format = {
  fill: "#E7F3F5",
  font: { bold: true, color: "#173B45" },
  verticalAlignment: "center",
  borders: { preset: "outside", style: "thin", color: "#9CB7BD" },
};
sheet.getRange("A2:I2").format.rowHeight = 30;
sheet.getRange("B2").format = { fill: "#FFF3B0", font: { bold: true, color: "#7A4E00" }, horizontalAlignment: "center" };
sheet.getRange("E2:I2").format.numberFormat = "¥#,##0.00;[Red]-¥#,##0.00";
sheet.getRange("E2").setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange("G2").setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange("I2").setNumberFormat("#,##0.00;[Red]-#,##0.00");

sheet.getRange("A4:I4").format = {
  fill: "#F3FAFB", font: { color: "#275D68", italic: true }, wrapText: true,
  horizontalAlignment: "left", verticalAlignment: "center",
};
sheet.getRange("A4:I4").format.rowHeight = 32;
sheet.getRange("A6:I6").format = {
  fill: "#FFF8D6", font: { color: "#7A5A00" }, wrapText: true,
  horizontalAlignment: "left", verticalAlignment: "center",
};
sheet.getRange("A6:I6").format.rowHeight = 26;

sheet.getRange("A8:I8").format = {
  fill: "#147D92",
  font: { bold: true, color: "#FFFFFF" },
  horizontalAlignment: "center",
  verticalAlignment: "center",
  wrapText: true,
  borders: { preset: "all", style: "thin", color: "#AFC7CC" },
};
sheet.getRange("A8:I8").format.rowHeight = 32;

sheet.getRange(`A${dataStart}:I${dataEnd}`).format = {
  borders: {
    insideHorizontal: { style: "thin", color: "#DDE8EA" },
    bottom: { style: "thin", color: "#AFC7CC" },
  },
  verticalAlignment: "center",
};
sheet.getRange(`A${dataStart}:A${dataEnd}`).format = { fill: "#F1F5F6", horizontalAlignment: "center", font: { color: "#53676B" } };
sheet.getRange(`B${dataStart}:B${dataEnd}`).format = { fill: "#FFF3B0", horizontalAlignment: "center", numberFormat: "0" };
sheet.getRange(`C${dataStart}:C${dataEnd}`).format = { fill: "#F1F5F6", horizontalAlignment: "center", numberFormat: "yyyy-mm-dd" };
sheet.getRange(`D${dataStart}:D${dataEnd}`).format = { wrapText: true };
sheet.getRange(`E${dataStart}:F${dataEnd}`).format = { fill: "#FFF8D6", numberFormat: "¥#,##0.00;[Red]-¥#,##0.00", horizontalAlignment: "right" };
sheet.getRange(`G${dataStart}:H${dataEnd}`).format = { wrapText: true };
sheet.getRange(`I${dataStart}:I${dataEnd}`).format = { fill: "#EAF4F6", font: { bold: true, color: "#174A55" }, numberFormat: "¥#,##0.00;[Red]-¥#,##0.00", horizontalAlignment: "right" };
sheet.getRange(`E${dataStart}:F${dataEnd}`).setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange(`I${dataStart}:I${dataEnd}`).setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange(`A${dataStart}:I${dataEnd}`).format.rowHeight = 22;

sheet.getRange(`B${dataStart}:B${dataEnd}`).dataValidation = {
  rule: { type: "whole", operator: "between", formula1: 101, formula2: 1231 },
};
sheet.getRange(`E${dataStart}:F${dataEnd}`).dataValidation = {
  rule: { type: "decimal", operator: "greaterThanOrEqual", formula1: 0 },
};

sheet.getRange("A1:A208").format.columnWidth = 8;
sheet.getRange("B1:B208").format.columnWidth = 12;
sheet.getRange("C1:C208").format.columnWidth = 15;
sheet.getRange("D1:D208").format.columnWidth = 24;
sheet.getRange("E1:F208").format.columnWidth = 15;
sheet.getRange("G1:G208").format.columnWidth = 13;
sheet.getRange("H1:H208").format.columnWidth = 25;
sheet.getRange("I1:I208").format.columnWidth = 17;
sheet.freezePanes.freezeRows(8);

const guide = workbook.worksheets.getItemAt(0);
for (const table of guide.tables.items) table.delete();
guide.deleteAllDrawings();
guide.getRange("A1:D30").unmerge();
guide.getRange("A1:D30").clear({ applyTo: "all" });
guide.name = "使用说明";
guide.showGridLines = false;
guide.getRange("A1:F1").merge();
guide.getRange("A1").values = [["备用金自动记账表｜使用说明"]];
guide.getRange("A3:F7").values = [
  ["1", "进入“备用金”工作表", null, null, null, null],
  ["2", "在黄色“日期数字”列输入月日，例如 715", null, null, null, null],
  ["3", "在黄色“收入”或“支出”列填金额", null, null, null, null],
  ["4", "日期、序号、累计收入、累计支出和结余自动计算", null, null, null, null],
  ["5", "下一年度使用时，把“年份”改成对应年份", null, null, null, null],
];
guide.getRange("A9:F9").merge();
guide.getRange("A9").values = [["原文件中的 U收支、房租水电工作表已保留，未改动。"]];
guide.getRange("A1:F1").format = { fill: "#0F4C5C", font: { bold: true, color: "#FFFFFF", size: 18 }, horizontalAlignment: "center" };
guide.getRange("A1:F1").format.rowHeight = 34;
guide.getRange("A3:A7").format = { fill: "#147D92", font: { bold: true, color: "#FFFFFF" }, horizontalAlignment: "center" };
guide.getRange("B3:F7").format = { fill: "#F3FAFB", font: { color: "#173B45" } };
guide.getRange("A3:F7").format.borders = { preset: "all", style: "thin", color: "#C8DADD" };
guide.getRange("A3:F7").format.rowHeight = 28;
guide.getRange("A9:F9").format = { fill: "#FFF8D6", font: { color: "#7A5A00" } };
guide.getRange("A1:A9").format.columnWidth = 7;
guide.getRange("B1:F9").format.columnWidth = 18;
guide.getRange("B1:B9").format.columnWidth = 48;

const output = await SpreadsheetFile.exportXlsx(workbook);
await output.save(outputPath);

const keyCheck = await workbook.inspect({
  kind: "table",
  range: "备用金!A1:I70",
  include: "values,formulas",
  tableMaxRows: 70,
  tableMaxCols: 9,
  maxChars: 18000,
});
console.log(keyCheck.ndjson);

const errors = await workbook.inspect({
  kind: "match",
  searchTerm: "#REF!|#DIV/0!|#VALUE!|#NAME\\?|#N/A",
  options: { useRegex: true, maxResults: 300 },
  summary: "final formula error scan",
});
console.log(errors.ndjson);

for (const finalSheet of workbook.worksheets.items) {
  const preview = await workbook.render({
    sheetName: finalSheet.name,
    ...(finalSheet.name === "备用金" ? { range: "A1:I70" } : { autoCrop: "all" }),
    scale: 1.2,
    format: "png",
  });
  await fs.writeFile(
    `${workDir}/final-${finalSheet.name.replaceAll("/", "-")}.png`,
    new Uint8Array(await preview.arrayBuffer()),
  );
}

console.log(JSON.stringify({ outputPath, transactionCount: transactions.length }));
