/**
 * AD-002 —— ModelsCatalog 的价格展示。
 *
 * 价格是网关产品的核心决策信息,而它在整条链路上被刻意保持为**精确十进制字符串**
 * (postgres NUMERIC → ::text → JSON string → 这里),中途任何一次 parseFloat 都会
 * 把这份精确性还回去(M-1:money 路径不走 float)。这些用例锁住两件事:
 *
 *   1. 渲染出的价格文本与后端给的字符串在数值上完全一致(只允许去尾随零)
 *   2. 无定价的模型不显示任何价格 —— 显示 0 会被读成「免费」
 */

import { describe, test, expect } from "vitest";
import { render, screen } from "@testing-library/react";

import { ModelsCatalog } from "@/components/business/ModelsCatalog";
import type { ModelEntry } from "@/lib/api/public-models";

function model(over: Partial<ModelEntry> = {}): ModelEntry {
  return {
    id: "qwen3.7-plus",
    object: "model",
    created: 1700000000,
    owned_by: "alibaba",
    capabilities: {
      chat: true,
      streaming: true,
      function_calling: true,
      vision: false,
      json_mode: true,
      context_window_tokens: 131072,
      max_output_tokens: 8192,
    },
    ...over,
  } as ModelEntry;
}

describe("ModelsCatalog 价格展示", () => {
  test("按后端给的十进制字符串渲染,去掉尾随零", () => {
    render(
      <ModelsCatalog
        models={[
          model({
            pricing: {
              input_per_1k_tokens: "0.000880",
              output_per_1k_tokens: "0.002200",
              currency: "USD",
            },
          }),
        ]}
      />,
    );
    expect(screen.getByText("$0.00088")).toBeDefined();
    expect(screen.getByText("$0.0022")).toBeDefined();
  });

  test("不引入浮点误差 —— 长小数原样保留", () => {
    // 0.1+0.2 那类误差一旦发生就会在这里显形。
    render(
      <ModelsCatalog
        models={[
          model({
            pricing: {
              input_per_1k_tokens: "0.000001",
              output_per_1k_tokens: "12.345678",
              currency: "USD",
            },
          }),
        ]}
      />,
    );
    expect(screen.getByText("$0.000001")).toBeDefined();
    expect(screen.getByText("$12.345678")).toBeDefined();
  });

  test("整数价格不被截成空", () => {
    // "10.000000" 去尾随零会先得到 "10.",小数点必须一并去掉。
    render(
      <ModelsCatalog
        models={[
          model({
            pricing: {
              input_per_1k_tokens: "10.000000",
              output_per_1k_tokens: "3",
              currency: "USD",
            },
          }),
        ]}
      />,
    );
    expect(screen.getByText("$10")).toBeDefined();
    expect(screen.getByText("$3")).toBeDefined();
  });

  test("未知币种回退为「币种码 + 空格 + 金额」,不静默丢失币种", () => {
    render(
      <ModelsCatalog
        models={[
          model({
            pricing: {
              input_per_1k_tokens: "0.500000",
              output_per_1k_tokens: "0.500000",
              currency: "EUR",
            },
          }),
        ]}
      />,
    );
    expect(screen.getAllByText("EUR 0.5").length).toBe(2);
  });

  test("无定价时不渲染任何价格区块 —— 0 会被读成免费", () => {
    render(<ModelsCatalog models={[model()]} />);
    expect(screen.queryByText(/Input \/ 1K/)).toBeNull();
    expect(screen.queryByText(/Output \/ 1K/)).toBeNull();
    expect(screen.queryByText(/\$/)).toBeNull();
  });
});
