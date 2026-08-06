import fs from "node:fs/promises";
import { FileBlob, SpreadsheetFile } from "@oai/artifact-tool";

const inputPath = "/Users/chanx/Desktop/BoostBrowser/outputs/019f8038-0a12-70d3-9be9-7db031ea188c/成都项目备用金-自动日期自动合计.xlsx";
const outputDir = "/Users/chanx/Desktop/BoostBrowser/outputs/019f8038-0a12-70d3-9be9-7db031ea188c";
const outputPath = `${outputDir}/成都项目备用金-U收支简洁版.xlsx`;
const previewDir = "/Users/chanx/Desktop/BoostBrowser/sheet_work";

const input = await FileBlob.load(inputPath);
const workbook = await SpreadsheetFile.importXlsx(input);
const sheet = workbook.worksheets.getItem("U收支");

const fundingRows = sheet.getRange("A3:J5").values;
const expenseRows = sheet.getRange("L3:V42").values;

function serialToDateCode(serial) {
  if (serial === null || serial === undefined || serial === "") return null;
  const date = new Date((Number(serial) - 25569) * 86400 * 1000);
  return (date.getUTCMonth() + 1) * 100 + date.getUTCDate();
}

function present(value) {
  return value !== null && value !== undefined && value !== "" && value !== "/";
}

function money(value) {
  const number = Number(value);
  return Number.isFinite(number) ? number.toFixed(2) : String(value ?? "");
}

const transactions = [];
let order = 0;

for (const row of fundingRows) {
  if (!present(row[7])) continue;
  const details = [];
  if (present(row[5])) details.push(`支付金额 ${money(row[5])}`);
  if (present(row[6])) details.push(`收款人 ${row[6]}`);
  if (present(row[8]) && Number(row[8]) !== 0) details.push(`手续费 ${money(row[8])}`);
  if (present(row[9])) details.push(row[9]);
  transactions.push({
    dateCode: serialToDateCode(row[1]),
    description: "U资金拨付",
    income: Number(row[7]) || 0,
    expense: null,
    handler: row[4] || "",
    currency: "USDT",
    note: details.join("；"),
    order: order++,
  });
}

for (const row of expenseRows) {
  if (!present(row[9]) && !present(row[2])) continue;
  const details = [];
  if (present(row[6]) || present(row[7])) {
    const quantity = present(row[6]) ? row[6] : "—";
    const unitPrice = present(row[7]) ? row[7] : "—";
    details.push(`数量 ${quantity} × 单价 ${unitPrice}`);
  }
  if (present(row[8]) && Number(row[8]) !== 0) details.push(`手续费 ${money(row[8])}`);
  if (present(row[10])) details.push(row[10]);
  if (present(row[0]) && !Number.isFinite(Number(row[0]))) details.push(`登记人 ${row[0]}`);
  transactions.push({
    dateCode: serialToDateCode(row[1]),
    description: row[2] || "",
    income: null,
    expense: Number(row[9]) || 0,
    handler: row[4] || "",
    currency: row[5] || "USDT",
    note: details.join("；"),
    order: order++,
  });
}

transactions.sort((a, b) => (a.dateCode ?? 99999) - (b.dateCode ?? 99999) || a.order - b.order);

for (const table of sheet.tables.items) table.delete();
sheet.deleteAllDrawings();
sheet.getRange("A1:V160").unmerge();
sheet.getRange("A1:V160").clear({ applyTo: "all" });
sheet.showGridLines = false;

const dataStart = 9;
const dataEnd = 128;

sheet.getRange("A1:J1").merge();
sheet.getRange("A1").values = [["成都项目 U 收支流水"]];
sheet.getRange("A2:J2").values = [["年份", 2026, "累计收入", null, "累计支出", null, "当前余额", null, "记录数", null]];
sheet.getRange("D2").formulas = [[`=ROUND(SUM(E${dataStart}:E${dataEnd}),2)`]];
sheet.getRange("F2").formulas = [[`=ROUND(SUM(F${dataStart}:F${dataEnd}),2)`]];
sheet.getRange("H2").formulas = [["=ROUND(D2-F2,2)"]];
sheet.getRange("J2").formulas = [[`=COUNTA(D${dataStart}:D${dataEnd})`]];

sheet.getRange("A4:J4").merge();
sheet.getRange("A4").values = [["录入方式：日期数字填 715 即自动显示 2026-07-15；金额填在“收入”或“支出”其中一列，余额会自动更新。"]];
sheet.getRange("A6:J6").merge();
sheet.getRange("A6").values = [["原表的支付金额、收款人、数量、单价和手续费均已保留在备注中，主表只保留常用字段。"]];
sheet.getRange("A8:J8").values = [[
  "序号", "日期数字", "日期（自动）", "用途说明", "收入（U）", "支出（U）", "经办人", "币种", "备注", "结余（自动）",
]];

const data = Array.from({ length: dataEnd - dataStart + 1 }, () => Array(10).fill(null));
transactions.forEach((item, index) => {
  data[index][1] = item.dateCode;
  data[index][3] = item.description;
  data[index][4] = item.income;
  data[index][5] = item.expense;
  data[index][6] = item.handler;
  data[index][7] = item.currency;
  data[index][8] = item.note;
});
sheet.getRange(`A${dataStart}:J${dataEnd}`).values = data;

sheet.getRange(`A${dataStart}`).formulas = [[`=IF(OR(B${dataStart}<>"",D${dataStart}<>"",E${dataStart}<>"",F${dataStart}<>"",G${dataStart}<>"",H${dataStart}<>"",I${dataStart}<>""),ROW()-8,"")`]];
sheet.getRange(`A${dataStart}:A${dataEnd}`).fillDown();
sheet.getRange(`C${dataStart}`).formulas = [[`=IF(B${dataStart}="","",DATE($B$2,INT(B${dataStart}/100),MOD(B${dataStart},100)))`]];
sheet.getRange(`C${dataStart}:C${dataEnd}`).fillDown();
sheet.getRange(`J${dataStart}`).formulas = [[`=IF(OR(B${dataStart}<>"",D${dataStart}<>"",E${dataStart}<>"",F${dataStart}<>"",G${dataStart}<>"",H${dataStart}<>"",I${dataStart}<>""),ROUND(SUM($E$${dataStart}:E${dataStart})-SUM($F$${dataStart}:F${dataStart}),2),"")`]];
sheet.getRange(`J${dataStart}:J${dataEnd}`).fillDown();

sheet.getRange("A1:J1").format = {
  fill: "#263B66",
  font: { bold: true, color: "#FFFFFF", size: 18 },
  horizontalAlignment: "center",
  verticalAlignment: "center",
};
sheet.getRange("A1:J1").format.rowHeight = 34;

sheet.getRange("A2:J2").format = {
  fill: "#EEF1F8",
  font: { bold: true, color: "#293957" },
  verticalAlignment: "center",
  borders: { preset: "outside", style: "thin", color: "#B8C1D8" },
};
sheet.getRange("A2:J2").format.rowHeight = 30;
sheet.getRange("B2").format = { fill: "#FFF3B0", font: { bold: true, color: "#7A4E00" }, horizontalAlignment: "center" };
sheet.getRange("D2").setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange("F2").setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange("H2").setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange("J2").setNumberFormat("0");

sheet.getRange("A4:J4").format = {
  fill: "#F5F7FC", font: { color: "#3D4D70", italic: true }, wrapText: true,
  horizontalAlignment: "left", verticalAlignment: "center",
};
sheet.getRange("A4:J4").format.rowHeight = 32;
sheet.getRange("A6:J6").format = {
  fill: "#FFF8D6", font: { color: "#7A5A00" }, wrapText: true,
  horizontalAlignment: "left", verticalAlignment: "center",
};
sheet.getRange("A6:J6").format.rowHeight = 28;

sheet.getRange("A8:J8").format = {
  fill: "#38558F",
  font: { bold: true, color: "#FFFFFF" },
  horizontalAlignment: "center",
  verticalAlignment: "center",
  wrapText: true,
  borders: { preset: "all", style: "thin", color: "#B8C1D8" },
};
sheet.getRange("A8:J8").format.rowHeight = 32;

sheet.getRange(`A${dataStart}:J${dataEnd}`).format = {
  borders: { insideHorizontal: { style: "thin", color: "#E1E5EF" }, bottom: { style: "thin", color: "#B8C1D8" } },
  verticalAlignment: "center",
};
sheet.getRange(`A${dataStart}:A${dataEnd}`).format = { fill: "#F2F4F8", horizontalAlignment: "center", font: { color: "#59647A" } };
sheet.getRange(`B${dataStart}:B${dataEnd}`).format = { fill: "#FFF3B0", horizontalAlignment: "center" };
sheet.getRange(`B${dataStart}:B${dataEnd}`).setNumberFormat("0");
sheet.getRange(`C${dataStart}:C${dataEnd}`).format = { fill: "#F2F4F8", horizontalAlignment: "center" };
sheet.getRange(`C${dataStart}:C${dataEnd}`).setNumberFormat("yyyy-mm-dd");
sheet.getRange(`D${dataStart}:D${dataEnd}`).format = { wrapText: true, horizontalAlignment: "left" };
sheet.getRange(`E${dataStart}:F${dataEnd}`).format = { fill: "#FFF8D6", horizontalAlignment: "right" };
sheet.getRange(`E${dataStart}:F${dataEnd}`).setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange(`G${dataStart}:H${dataEnd}`).format = { horizontalAlignment: "center" };
sheet.getRange(`I${dataStart}:I${dataEnd}`).format = { wrapText: true, horizontalAlignment: "left", font: { color: "#465268" } };
sheet.getRange(`J${dataStart}:J${dataEnd}`).format = { fill: "#EDF1F8", font: { bold: true, color: "#2C416E" }, horizontalAlignment: "right" };
sheet.getRange(`J${dataStart}:J${dataEnd}`).setNumberFormat("#,##0.00;[Red]-#,##0.00");
sheet.getRange(`A${dataStart}:J${dataEnd}`).format.rowHeight = 24;
transactions.forEach((item, index) => {
  const row = dataStart + index;
  const height = item.note.length > 55 ? 56 : item.note.length > 24 ? 40 : 28;
  sheet.getRange(`A${row}:J${row}`).format.rowHeight = height;
});

sheet.getRange(`B${dataStart}:B${dataEnd}`).dataValidation = {
  rule: { type: "whole", operator: "between", formula1: 101, formula2: 1231 },
};
sheet.getRange(`E${dataStart}:F${dataEnd}`).dataValidation = {
  rule: { type: "decimal", operator: "greaterThanOrEqual", formula1: 0 },
};
sheet.getRange(`H${dataStart}:H${dataEnd}`).dataValidation = {
  rule: { type: "list", values: ["USDT", "BNB", "USDC", "其他"] },
};

sheet.getRange("A1:A128").format.columnWidth = 7;
sheet.getRange("B1:B128").format.columnWidth = 11;
sheet.getRange("C1:C128").format.columnWidth = 15;
sheet.getRange("D1:D128").format.columnWidth = 25;
sheet.getRange("E1:F128").format.columnWidth = 14;
sheet.getRange("G1:G128").format.columnWidth = 12;
sheet.getRange("H1:H128").format.columnWidth = 10;
sheet.getRange("I1:I128").format.columnWidth = 38;
sheet.getRange("J1:J128").format.columnWidth = 16;
sheet.freezePanes.freezeRows(8);

const guide = workbook.worksheets.getItem("使用说明");
guide.getRange("A9:F9").unmerge();
guide.getRange("A9:F9").merge();
guide.getRange("A9").values = [["备用金、U收支工作表已整理为自动流水表；房租水电工作表保留原表。"]];
guide.getRange("A9:F9").format = { fill: "#FFF8D6", font: { color: "#7A5A00" } };

await fs.mkdir(outputDir, { recursive: true });
const output = await SpreadsheetFile.exportXlsx(workbook);
await output.save(outputPath);

const keyCheck = await workbook.inspect({
  kind: "table",
  range: "U收支!A1:J58",
  include: "values,formulas",
  tableMaxRows: 58,
  tableMaxCols: 10,
  maxChars: 16000,
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
  const range = finalSheet.name === "U收支" ? "A1:J58" : finalSheet.name === "备用金" ? "A1:I70" : undefined;
  const preview = await workbook.render({
    sheetName: finalSheet.name,
    ...(range ? { range } : { autoCrop: "all" }),
    scale: 1.2,
    format: "png",
  });
  await fs.writeFile(
    `${previewDir}/u-final-${finalSheet.name.replaceAll("/", "-")}.png`,
    new Uint8Array(await preview.arrayBuffer()),
  );
}

console.log(JSON.stringify({ outputPath, transactionCount: transactions.length }));
