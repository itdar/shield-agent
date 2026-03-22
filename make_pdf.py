#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""rua / mcp-shield 프로젝트 설명서 PDF 생성기"""

from reportlab.lib.pagesizes import A4
from reportlab.lib import colors
from reportlab.lib.units import mm
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.platypus import (
    SimpleDocTemplate, Paragraph, Spacer, Table, TableStyle,
    HRFlowable, PageBreak, KeepTogether
)
from reportlab.platypus.flowables import Flowable
from reportlab.lib.enums import TA_LEFT, TA_CENTER, TA_RIGHT
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
import os, subprocess

# ── 한글 폰트 설정 ─────────────────────────────────────────────────────────────
FONT_PATHS = [
    "/System/Library/Fonts/AppleSDGothicNeo.ttc",
    "/Library/Fonts/NanumGothic.ttf",
    "/System/Library/Fonts/Supplemental/AppleGothic.ttf",
]

def register_korean_font():
    for path in FONT_PATHS:
        if os.path.exists(path):
            try:
                pdfmetrics.registerFont(TTFont("Korean", path))
                pdfmetrics.registerFont(TTFont("Korean-Bold", path))
                return "Korean"
            except Exception:
                continue
    return "Helvetica"

FONT = register_korean_font()
FONT_BOLD = FONT  # same TTF handles bold

W, H = A4  # 595 x 842 pt

# ── 색상 팔레트 ────────────────────────────────────────────────────────────────
C_BLUE       = colors.HexColor("#1565C0")   # 진파랑
C_BLUE_LT    = colors.HexColor("#E3F2FD")   # 연파랑
C_BLUE_MID   = colors.HexColor("#42A5F5")   # 중간파랑
C_ORANGE     = colors.HexColor("#E65100")   # 주황
C_ORANGE_LT  = colors.HexColor("#FFF3E0")   # 연주황
C_GREEN      = colors.HexColor("#2E7D32")   # 초록
C_GREEN_LT   = colors.HexColor("#E8F5E9")   # 연초록
C_RED        = colors.HexColor("#C62828")   # 빨강
C_RED_LT     = colors.HexColor("#FFEBEE")   # 연빨강
C_GRAY       = colors.HexColor("#546E7A")   # 회색
C_GRAY_LT    = colors.HexColor("#ECEFF1")   # 연회색
C_BG         = colors.HexColor("#FAFAFA")   # 배경
C_WHITE      = colors.white
C_BLACK      = colors.HexColor("#212121")
C_YELLOW     = colors.HexColor("#F9A825")   # 노랑
C_PURPLE     = colors.HexColor("#6A1B9A")   # 보라
C_PURPLE_LT  = colors.HexColor("#F3E5F5")

# ── 스타일 ────────────────────────────────────────────────────────────────────
def make_styles():
    base = getSampleStyleSheet()
    s = {}

    def PS(name, **kw):
        defaults = dict(fontName=FONT, fontSize=10, leading=15,
                        textColor=C_BLACK, spaceAfter=4)
        defaults.update(kw)
        return ParagraphStyle(name, **defaults)

    s["cover_title"]  = PS("cover_title",  fontSize=32, leading=40,
                            fontName=FONT_BOLD, textColor=C_WHITE,
                            alignment=TA_CENTER, spaceAfter=8)
    s["cover_sub"]    = PS("cover_sub",    fontSize=18, leading=26,
                            textColor=C_BLUE_LT, alignment=TA_CENTER)
    s["cover_badge"]  = PS("cover_badge",  fontSize=11, leading=16,
                            textColor=C_WHITE, alignment=TA_CENTER, spaceAfter=0)

    s["h1"]  = PS("h1",  fontSize=20, leading=26, fontName=FONT_BOLD,
                  textColor=C_WHITE, spaceAfter=0, spaceBefore=6)
    s["h2"]  = PS("h2",  fontSize=15, leading=20, fontName=FONT_BOLD,
                  textColor=C_BLUE, spaceAfter=4, spaceBefore=10)
    s["h3"]  = PS("h3",  fontSize=12, leading=17, fontName=FONT_BOLD,
                  textColor=C_GRAY, spaceAfter=3, spaceBefore=7)

    s["body"] = PS("body", fontSize=10, leading=16, spaceAfter=5)
    s["body_c"] = PS("body_c", fontSize=10, leading=16, alignment=TA_CENTER)
    s["small"] = PS("small", fontSize=8.5, leading=13, textColor=C_GRAY)

    s["code"]  = PS("code", fontName="Courier", fontSize=8.5, leading=13,
                    textColor=C_BLUE, backColor=colors.HexColor("#F8F9FA"),
                    leftIndent=6, rightIndent=6, spaceAfter=4)

    s["call"] = PS("call", fontSize=10, leading=15,
                   textColor=C_ORANGE, fontName=FONT_BOLD)

    s["tip"]  = PS("tip", fontSize=10, leading=15,
                   textColor=C_GREEN, backColor=C_GREEN_LT,
                   leftIndent=8, rightIndent=8, spaceAfter=4)

    s["warn"] = PS("warn", fontSize=10, leading=15,
                   textColor=C_RED, backColor=C_RED_LT,
                   leftIndent=8, rightIndent=8, spaceAfter=4)

    s["tbl_hdr"] = PS("tbl_hdr", fontSize=9.5, leading=13,
                       fontName=FONT_BOLD, textColor=C_WHITE,
                       alignment=TA_CENTER)
    s["tbl_cell"] = PS("tbl_cell", fontSize=9, leading=13, textColor=C_BLACK)
    s["tbl_cell_c"] = PS("tbl_cell_c", fontSize=9, leading=13,
                          textColor=C_BLACK, alignment=TA_CENTER)
    s["tbl_mono"] = PS("tbl_mono", fontName="Courier", fontSize=8.5,
                        leading=13, textColor=C_BLUE)
    return s

S = make_styles()

# ── 커스텀 Flowable : 구분선 제목 ─────────────────────────────────────────────
class SectionHeader(Flowable):
    def __init__(self, text, color=C_BLUE, w=None):
        super().__init__()
        self.text  = text
        self.color = color
        self.bw    = w or (W - 40*mm)
        self.height = 28

    def wrap(self, aw, ah):
        return self.bw, self.height

    def draw(self):
        c = self.canv
        c.setFillColor(self.color)
        c.roundRect(0, 0, self.bw, self.height, 6, fill=1, stroke=0)
        c.setFillColor(C_WHITE)
        c.setFont(FONT_BOLD, 14)
        c.drawString(10, 8, self.text)


class SubHeader(Flowable):
    def __init__(self, text, color=C_BLUE_MID):
        super().__init__()
        self.text  = text
        self.color = color
        self.height = 22

    def wrap(self, aw, ah):
        return aw, self.height

    def draw(self):
        c = self.canv
        c.setFillColor(self.color)
        c.roundRect(0, 0, 4, self.height, 2, fill=1, stroke=0)
        c.setFillColor(self.color)
        c.setFont(FONT_BOLD, 12)
        c.drawString(12, 6, self.text)


# ── 커스텀 Flowable : 아키텍처 다이어그램 ─────────────────────────────────────
class ArchDiagram(Flowable):
    """AI에이전트 → mcp-shield → MCP서버 전체 흐름 다이어그램"""
    def __init__(self, w=None):
        super().__init__()
        self.dw = w or (W - 40*mm)
        self.dh = 200

    def wrap(self, aw, ah):
        return self.dw, self.dh

    def draw(self):
        c = self.canv
        dw, dh = self.dw, self.dh

        def box(x, y, w, h, label, sublabel="", fill=C_BLUE, text=C_WHITE, r=8):
            c.setFillColor(fill)
            c.setStrokeColor(colors.HexColor("#B0BEC5"))
            c.roundRect(x, y, w, h, r, fill=1, stroke=1)
            c.setFillColor(text)
            c.setFont(FONT_BOLD, 10)
            ty = y + h/2 + (5 if sublabel else 0)
            c.drawCentredString(x + w/2, ty, label)
            if sublabel:
                c.setFont(FONT, 8)
                c.setFillColor(colors.HexColor("#CFD8DC") if fill == C_BLUE else C_GRAY)
                c.drawCentredString(x + w/2, y + h/2 - 8, sublabel)

        def arrow(x1, y1, x2, y2, label="", color=C_BLUE_MID):
            c.setStrokeColor(color)
            c.setLineWidth(2)
            c.line(x1, y1, x2, y2)
            # arrowhead
            c.setFillColor(color)
            dx = x2-x1; dy = y2-y1
            import math
            l = math.sqrt(dx*dx+dy*dy) or 1
            ux, uy = dx/l, dy/l
            px, py = -uy, ux
            sz = 7
            p = c.beginPath()
            p.moveTo(x2, y2)
            p.lineTo(x2 - sz*ux + sz*0.4*px, y2 - sz*uy + sz*0.4*py)
            p.lineTo(x2 - sz*ux - sz*0.4*px, y2 - sz*uy - sz*0.4*py)
            p.close()
            c.drawPath(p, fill=1, stroke=0)
            if label:
                mx, my = (x1+x2)/2, (y1+y2)/2
                c.setFillColor(C_GRAY)
                c.setFont(FONT, 7.5)
                c.drawCentredString(mx, my+4, label)

        bw, bh = 110, 50

        # AI 에이전트
        ax = 10; ay = dh/2 - bh/2
        box(ax, ay, bw, bh, "AI 에이전트", "Claude / GPT", fill=C_PURPLE, r=10)

        # mcp-shield (중앙, 크게)
        mx = dw/2 - 80; my = dh/2 - 55
        box(mx, my, 160, 110, "", fill=C_BLUE_LT, text=C_BLUE, r=10)
        c.setFillColor(C_BLUE)
        c.setFont(FONT_BOLD, 12)
        c.drawCentredString(mx+80, my+82, "mcp-shield")
        c.setFont(FONT, 8)
        c.setFillColor(C_GRAY)
        c.drawCentredString(mx+80, my+67, "(rua 프로젝트)")

        # 내부 미들웨어 박스들
        iw, ih = 60, 22
        gap = 8
        ix1 = mx + (160 - iw*2 - gap)/2
        ix2 = ix1 + iw + gap
        iy = my + 30

        box(ix1, iy, iw, ih, "Auth", "서명검증", fill=C_ORANGE, text=C_WHITE, r=5)
        box(ix2, iy, iw, ih, "Log", "SQLite", fill=C_GREEN, text=C_WHITE, r=5)

        c.setStrokeColor(C_BLUE_MID)
        c.setLineWidth(1.5)
        c.setDash([4,2])
        c.line(ix1+iw+1, iy+ih/2, ix2-1, iy+ih/2)
        c.setDash()

        # MCP 서버
        rx = dw - bw - 10; ry = dh/2 - bh/2
        box(rx, ry, bw, bh, "MCP 서버", "python / node", fill=C_GREEN, r=10)

        # 화살표 AI → shield
        arrow(ax+bw, ay+bh*0.6, mx-1, my+55+bh*0.1, "stdin\n요청", color=C_BLUE_MID)
        arrow(mx-1, my+55+bh*0.6, ax+bw, ay+bh*0.4, "stdout\n응답", color=C_PURPLE)

        # 화살표 shield → MCP
        arrow(mx+160, my+55+bh*0.1, rx-1, ry+bh*0.6, "stdin\n전달", color=C_BLUE_MID)
        arrow(rx-1, ry+bh*0.4, mx+160, my+55+bh*0.6, "stdout\n응답", color=C_GREEN)

        # 모니터 서버
        monx = dw/2 - 45; mony = 4
        box(monx, mony, 90, 22, "모니터 :9090", fill=C_GRAY, text=C_WHITE, r=5)
        c.setStrokeColor(C_GRAY)
        c.setLineWidth(1)
        c.setDash([3,3])
        c.line(dw/2, mony+22, dw/2, my)
        c.setDash()
        c.setFillColor(C_GRAY)
        c.setFont(FONT, 7)
        c.drawCentredString(dw/2 + 20, mony+34, "/healthz  /metrics")


class PipelineFlow(Flowable):
    """요청 처리 파이프라인 흐름도"""
    def __init__(self, w=None):
        super().__init__()
        self.dw = w or (W - 40*mm)
        self.dh = 130

    def wrap(self, aw, ah):
        return self.dw, self.dh

    def draw(self):
        c = self.canv
        dw, dh = self.dw, self.dh

        steps = [
            ("AI 에이전트\nstdin 전송", C_PURPLE),
            ("PipelineIn\n파싱", C_BLUE_MID),
            ("Auth MW\n서명 검증", C_ORANGE),
            ("Log MW\nSQLite 저장", C_GREEN),
            ("MCP 서버\n처리", C_GRAY),
        ]
        n = len(steps)
        bw = (dw - 10) / n - 8
        bh = 52
        by = dh/2 - bh/2

        for i, (label, color) in enumerate(steps):
            x = i * (bw + 8) + 5
            c.setFillColor(color)
            c.setStrokeColor(C_WHITE)
            c.roundRect(x, by, bw, bh, 6, fill=1, stroke=0)
            c.setFillColor(C_WHITE)
            c.setFont(FONT_BOLD, 8.5)
            lines = label.split("\n")
            if len(lines) == 2:
                c.drawCentredString(x + bw/2, by + bh/2 + 5, lines[0])
                c.setFont(FONT, 7.5)
                c.setFillColor(colors.HexColor("#CFD8DC"))
                c.drawCentredString(x + bw/2, by + bh/2 - 8, lines[1])
            else:
                c.drawCentredString(x + bw/2, by + bh/2, label)

            # 화살표
            if i < n-1:
                ax = x + bw + 1
                ay = by + bh/2
                c.setFillColor(C_BLUE_MID)
                c.setStrokeColor(C_BLUE_MID)
                c.setLineWidth(2)
                c.line(ax, ay, ax+6, ay)
                p = c.beginPath()
                p.moveTo(ax+7, ay)
                p.lineTo(ax+2, ay+4)
                p.lineTo(ax+2, ay-4)
                p.close()
                c.drawPath(p, fill=1, stroke=0)

        # 위에 레이블
        c.setFillColor(C_BLUE)
        c.setFont(FONT_BOLD, 9)
        c.drawCentredString(dw/2, dh - 8, "▼  요청(Request) 처리 흐름  ▼")

        # 아래 역방향 표시
        c.setFillColor(C_GREEN)
        c.setFont(FONT, 8)
        c.drawCentredString(dw/2, 6, "◀  응답(Response)은 역방향으로: MCP서버 → Log MW → AI 에이전트  ▶")


class MiddlewareChain(Flowable):
    """미들웨어 체인 구조 그림"""
    def __init__(self, w=None):
        super().__init__()
        self.dw = w or (W - 40*mm)
        self.dh = 160

    def wrap(self, aw, ah):
        return self.dw, self.dh

    def draw(self):
        c = self.canv
        dw, dh = self.dw, self.dh

        # 인터페이스 박스
        iw, ih = 200, 40
        ix = (dw - iw)/2
        iy = dh - ih - 5

        c.setFillColor(C_BLUE_LT)
        c.setStrokeColor(C_BLUE)
        c.setLineWidth(1.5)
        c.roundRect(ix, iy, iw, ih, 6, fill=1, stroke=1)
        c.setFillColor(C_BLUE)
        c.setFont(FONT_BOLD, 10)
        c.drawCentredString(dw/2, iy+ih-16, "Middleware (인터페이스)")
        c.setFont("Courier", 8)
        c.setFillColor(C_GRAY)
        c.drawCentredString(dw/2, iy+6, "ProcessRequest() / ProcessResponse()")

        # 화살표 다운
        c.setStrokeColor(C_BLUE_MID)
        c.setLineWidth(1.5)
        c.line(dw/2 - 60, iy, dw/2 - 60, iy-15)
        c.line(dw/2 + 60, iy, dw/2 + 60, iy-15)

        # 구현체 2개
        bw, bh = 155, 55
        gap = 20
        x1 = dw/2 - bw - gap/2
        x2 = dw/2 + gap/2
        by = iy - bh - 15

        def impl_box(x, title, items, color, ltcolor):
            c.setFillColor(ltcolor)
            c.setStrokeColor(color)
            c.setLineWidth(1.5)
            c.roundRect(x, by, bw, bh, 6, fill=1, stroke=1)
            c.setFillColor(color)
            c.roundRect(x, by+bh-18, bw, 18, 6, fill=1, stroke=0)
            c.roundRect(x, by+bh-18, bw, 8, 0, fill=1, stroke=0)
            c.setFillColor(C_WHITE)
            c.setFont(FONT_BOLD, 9.5)
            c.drawCentredString(x + bw/2, by+bh-13, title)
            c.setFillColor(colors.HexColor("#424242"))
            c.setFont(FONT, 8.5)
            for i, item in enumerate(items):
                c.drawString(x+8, by+bh-30 - i*14, "• " + item)

        impl_box(x1, "AuthMiddleware",
                 ["Ed25519 서명 검증", "키스토어 조회", "open / closed 모드"],
                 C_ORANGE, C_ORANGE_LT)

        impl_box(x2, "LogMiddleware",
                 ["SQLite 비동기 저장", "Telemetry 전송", "레이턴시 계산"],
                 C_GREEN, C_GREEN_LT)

        # PassthroughMiddleware (임베딩)
        pw, ph = 120, 22
        px = (dw - pw)/2
        py = by - ph - 8
        c.setFillColor(C_GRAY_LT)
        c.setStrokeColor(C_GRAY)
        c.setLineWidth(1)
        c.roundRect(px, py, pw, ph, 4, fill=1, stroke=1)
        c.setFillColor(C_GRAY)
        c.setFont(FONT, 8)
        c.drawCentredString(dw/2, py+7, "PassthroughMiddleware (no-op 기본)")

        # 점선 연결
        c.setDash([3,2])
        c.setStrokeColor(C_GRAY)
        c.line(x1+bw/2, by, dw/2-20, py+ph)
        c.line(x2+bw/2, by, dw/2+20, py+ph)
        c.setDash()


class FolderTree(Flowable):
    """폴더 구조 시각화"""
    def __init__(self, w=None):
        super().__init__()
        self.dw = w or (W - 40*mm)
        self.dh = 320

    def wrap(self, aw, ah):
        return self.dw, self.dh

    def draw(self):
        c = self.canv
        dw, dh = self.dw, self.dh

        entries = [
            (0, "rua/",                      C_BLUE,       "프로젝트 루트"),
            (1, "cmd/mcp-shield/",           C_BLUE_MID,   "실행 파일 진입점"),
            (2, "main.go",                   C_ORANGE,     "여기서 시작! 모든 것을 조립"),
            (1, "internal/",                 C_BLUE_MID,   "핵심 로직 (외부 비공개)"),
            (2, "auth/",                     C_ORANGE,     "Ed25519 서명 검증"),
            (2, "config/",                   C_GREEN,      "YAML 설정 로딩"),
            (2, "jsonrpc/",                  C_PURPLE,     "JSON-RPC 메시지 파싱"),
            (2, "logging/",                  C_GRAY,       "slog 로거 초기화"),
            (2, "middleware/",               C_BLUE,       "미들웨어 인터페이스 & 체인"),
            (2, "monitor/",                  C_GREEN,      "HTTP /healthz /metrics"),
            (2, "process/",                  C_ORANGE,     "자식 프로세스 실행 & 파이프"),
            (2, "storage/",                  C_BLUE,       "SQLite 저장소"),
            (2, "telemetry/",                C_PURPLE,     "익명 사용 통계 전송"),
            (1, "go.mod",                    C_GRAY,       "의존성 목록 (package.json 같은 것)"),
            (1, "mcp-shield.example.yaml",   C_GRAY,       "설정 파일 예시"),
        ]

        row_h = 18
        indent = 22
        x0 = 10
        y = dh - row_h - 4

        for depth, name, color, desc in entries:
            x = x0 + depth * indent
            is_dir = name.endswith("/")

            # 아이콘
            icon = "📁" if is_dir else "📄"
            isize = 11
            c.setFillColor(color)
            c.setFont(FONT, isize)
            # 연결선
            if depth > 0:
                c.setStrokeColor(colors.HexColor("#B0BEC5"))
                c.setLineWidth(0.8)
                c.line(x0 + (depth-1)*indent + 10, y + row_h/2,
                       x, y + row_h/2)
                c.line(x0 + (depth-1)*indent + 10, y + row_h,
                       x0 + (depth-1)*indent + 10, y + row_h/2)

            # 배지 배경
            if depth == 0:
                c.setFillColor(color)
                c.roundRect(x-2, y+1, 150, row_h-2, 4, fill=1, stroke=0)
                c.setFillColor(C_WHITE)
            elif is_dir:
                c.setFillColor(colors.HexColor("#E8EAF6"))
                c.roundRect(x-2, y+1, 130, row_h-2, 3, fill=1, stroke=0)
                c.setFillColor(color)
            else:
                c.setFillColor(color)

            c.setFont(FONT_BOLD if depth <= 1 else FONT, 9 if depth == 0 else 8.5)
            c.drawString(x + 2, y + 4, name)

            # 설명
            c.setFillColor(C_GRAY)
            c.setFont(FONT, 8)
            c.drawString(x + 140 if depth == 0 else x + 118, y + 4, "← " + desc)

            y -= row_h


class ConfigPriority(Flowable):
    """설정 우선순위 다이어그램"""
    def __init__(self, w=None):
        super().__init__()
        self.dw = w or (W - 40*mm)
        self.dh = 80

    def wrap(self, aw, ah):
        return self.dw, self.dh

    def draw(self):
        c = self.canv
        dw, dh = self.dw, self.dh

        levels = [
            ("CLI 플래그\n--log-level", C_RED,      "최우선"),
            ("환경변수\nMCP_SHIELD_*", C_ORANGE,    "2순위"),
            ("YAML 파일\nmcp-shield.yaml", C_BLUE,  "3순위"),
            ("기본값\n(내장)",           C_GRAY,    "최하위"),
        ]

        n = len(levels)
        bw = (dw - 20) / n - 10
        for i, (label, color, rank) in enumerate(levels):
            h = dh - 10 - i * 8  # 피라미드처럼 크기 변화
            x = 10 + i * (bw + 10)
            by = (dh - h) / 2

            c.setFillColor(color)
            c.setStrokeColor(C_WHITE)
            c.roundRect(x, by, bw, h, 6, fill=1, stroke=0)
            c.setFillColor(C_WHITE)
            c.setFont(FONT_BOLD, 8.5)
            lines = label.split("\n")
            c.drawCentredString(x+bw/2, by+h/2+3, lines[0])
            c.setFont("Courier", 7.5)
            c.setFillColor(colors.HexColor("#CFD8DC"))
            c.drawCentredString(x+bw/2, by+h/2-9, lines[1])

            # 순위 배지
            c.setFillColor(C_WHITE)
            c.setFont(FONT_BOLD, 7)
            c.setFillColor(color)
            c.roundRect(x+bw/2-18, by-14, 36, 12, 4, fill=1, stroke=0)
            c.setFillColor(C_WHITE)
            c.drawCentredString(x+bw/2, by-6, rank)

            # 화살표
            if i < n-1:
                ax = x + bw + 2
                ay = dh/2
                c.setFillColor(C_BLUE_MID)
                c.setStrokeColor(C_BLUE_MID)
                c.setLineWidth(1.5)
                p = c.beginPath()
                p.moveTo(ax+7, ay)
                p.lineTo(ax+1, ay+4)
                p.lineTo(ax+1, ay-4)
                p.close()
                c.drawPath(p, fill=1, stroke=0)


class AuthFlow(Flowable):
    """인증 흐름 다이어그램"""
    def __init__(self, w=None):
        super().__init__()
        self.dw = w or (W - 40*mm)
        self.dh = 180

    def wrap(self, aw, ah):
        return self.dw, self.dh

    def draw(self):
        c = self.canv
        dw, dh = self.dw, self.dh

        def box(x, y, w, h, text, color, tc=C_WHITE, r=5):
            c.setFillColor(color)
            c.roundRect(x, y, w, h, r, fill=1, stroke=0)
            c.setFillColor(tc)
            c.setFont(FONT, 8.5)
            lines = text.split("\n")
            for i, l in enumerate(lines):
                c.drawCentredString(x+w/2, y+h/2+(len(lines)-1-i)*10-4, l)

        def arr(x1, y1, x2, y2, lbl=""):
            c.setStrokeColor(C_GRAY)
            c.setLineWidth(1.3)
            c.line(x1, y1, x2, y2)
            c.setFillColor(C_GRAY)
            p = c.beginPath()
            if x2 > x1:
                p.moveTo(x2, y2); p.lineTo(x2-6, y2+3); p.lineTo(x2-6, y2-3)
            else:
                p.moveTo(x2, y2); p.lineTo(x2+6, y2+3); p.lineTo(x2+6, y2-3)
            p.close(); c.drawPath(p, fill=1, stroke=0)
            if lbl:
                c.setFont(FONT, 7.5); c.setFillColor(C_GRAY)
                c.drawCentredString((x1+x2)/2, (y1+y2)/2+5, lbl)

        bw, bh = 95, 34
        # 요청 들어옴
        box(5, dh-bh-5, bw, bh, "JSON-RPC\n요청 수신", C_BLUE_MID)

        # 서명 있음?
        dx, dy = 5+bw+10, dh-50
        box(dx, dy, 100, 42, "서명\n(_mcp_agent_id\n있음?)", C_GRAY, r=8)

        arr(5+bw, dh-bh/2-5, dx, dy+21, "")

        # YES / NO 분기
        # NO → unsigned
        box(dx+110, dy+10, 80, 22, "unsigned → 통과", C_GREEN_LT, tc=C_GREEN)
        c.setFont(FONT_BOLD, 8); c.setFillColor(C_RED)
        c.drawString(dx+102, dy+18, "NO")
        arr(dx+100, dy+21, dx+110, dy+21, "")

        # YES → 키 조회
        kx, ky = dx, dy-55
        box(kx, ky, 100, 32, "공개키 조회\n(KeyStore)", C_BLUE)
        c.setFont(FONT_BOLD, 8); c.setFillColor(C_GREEN)
        c.drawCentredString(dx+50, dy-4, "YES")
        arr(dx+50, dy, dx+50, ky+32, "")

        # 키 없음
        box(kx+110, ky+5, 90, 22, "키 없음 → fail", C_RED_LT, tc=C_RED)
        arr(kx+100, ky+16, kx+110, ky+16, "못찾으면")

        # 서명 검증
        vx, vy = kx, ky-52
        box(vx, vy, 100, 32, "Ed25519\n서명 검증", C_ORANGE)
        arr(kx+50, ky, kx+50, vy+32, "")

        # 검증 성공
        box(vx+110, vy+5, 90, 22, "verified ✓", C_GREEN_LT, tc=C_GREEN)
        arr(vx+100, vy+16, vx+110, vy+16, "성공")

        # 검증 실패
        box(vx, vy-32, 100, 22, "failed / closed면 거부", C_RED_LT, tc=C_RED)
        arr(vx+50, vy, vx+50, vy-10, "실패")


# ── 표 헬퍼 ──────────────────────────────────────────────────────────────────
def make_table(headers, rows, col_widths, hdr_color=C_BLUE):
    data = [[Paragraph(h, S["tbl_hdr"]) for h in headers]]
    for row in rows:
        data.append([Paragraph(str(cell), S["tbl_cell"]) for cell in row])

    ts = TableStyle([
        ("BACKGROUND",  (0,0), (-1,0),  hdr_color),
        ("ROWBACKGROUNDS", (0,1), (-1,-1), [C_WHITE, C_GRAY_LT]),
        ("GRID",        (0,0), (-1,-1),  0.5, colors.HexColor("#CFD8DC")),
        ("TOPPADDING",  (0,0), (-1,-1),  5),
        ("BOTTOMPADDING",(0,0),(-1,-1),  5),
        ("LEFTPADDING", (0,0), (-1,-1),  6),
        ("RIGHTPADDING",(0,0), (-1,-1),  6),
        ("VALIGN",      (0,0), (-1,-1),  "MIDDLE"),
        ("FONTNAME",    (0,0), (-1,0),   FONT_BOLD),
        ("FONTNAME",    (0,1), (-1,-1),  FONT),
        ("FONTSIZE",    (0,0), (-1,-1),  9),
        ("ROWBACKGROUNDS", (0,0), (-1,0), [hdr_color]),
    ])
    return Table(data, colWidths=col_widths, style=ts, repeatRows=1)


def code_block(text):
    lines = text.strip().split("\n")
    data = [[Paragraph(l, S["code"])] for l in lines]
    ts = TableStyle([
        ("BACKGROUND", (0,0), (-1,-1), colors.HexColor("#F0F4F8")),
        ("BOX",        (0,0), (-1,-1), 1, colors.HexColor("#B0BEC5")),
        ("LEFTPADDING",(0,0), (-1,-1), 8),
        ("RIGHTPADDING",(0,0),(-1,-1), 8),
        ("TOPPADDING", (0,0), (-1,-1), 3),
        ("BOTTOMPADDING",(0,0),(-1,-1),3),
    ])
    return Table(data, colWidths=[W - 40*mm - 20], style=ts)


def info_box(text, color=C_BLUE_LT, border=C_BLUE, icon="💡"):
    data = [[Paragraph(f"{icon}  {text}", S["body"])]]
    ts = TableStyle([
        ("BACKGROUND", (0,0), (-1,-1), color),
        ("BOX",        (0,0), (-1,-1), 1.5, border),
        ("LEFTPADDING",(0,0), (-1,-1), 10),
        ("RIGHTPADDING",(0,0),(-1,-1), 10),
        ("TOPPADDING", (0,0), (-1,-1), 7),
        ("BOTTOMPADDING",(0,0),(-1,-1),7),
        ("VALIGN",     (0,0), (-1,-1), "MIDDLE"),
    ])
    return Table(data, colWidths=[W - 40*mm - 20], style=ts)


# ── 페이지 헤더/푸터 ──────────────────────────────────────────────────────────
def on_page(canvas, doc):
    canvas.saveState()
    # 푸터
    canvas.setFillColor(C_GRAY)
    canvas.setFont(FONT, 8)
    canvas.drawCentredString(W/2, 15, f"rua / mcp-shield 프로젝트 가이드  |  {doc.page} 페이지")
    canvas.setStrokeColor(colors.HexColor("#B0BEC5"))
    canvas.setLineWidth(0.5)
    canvas.line(20*mm, 20, W-20*mm, 20)
    canvas.restoreState()


def cover_page(canvas, doc):
    canvas.saveState()
    # 배경 그라데이션 효과 (사각형 여러 개)
    for i in range(30):
        ratio = i / 30
        r = int(21 * (1-ratio) + 13 * ratio) / 255
        g = int(101 * (1-ratio) + 71 * ratio) / 255
        b = int(192 * (1-ratio) + 161 * ratio) / 255
        canvas.setFillColorRGB(r, g, b)
        canvas.rect(0, H/30*i, W, H/30+1, fill=1, stroke=0)

    # 장식 원
    canvas.setFillColor(colors.HexColor("#1976D2"))
    canvas.circle(W-60, H-60, 100, fill=1, stroke=0)
    canvas.setFillColor(colors.HexColor("#1565C0"))
    canvas.circle(60, 60, 70, fill=1, stroke=0)
    canvas.setFillColor(colors.HexColor("#0D47A1"))
    canvas.circle(W/2, 30, 50, fill=1, stroke=0)
    canvas.restoreState()


# ── 메인 빌드 ─────────────────────────────────────────────────────────────────
def build():
    out = "/Users/gino/Gin/src/RaaS/EASY.pdf"
    doc = SimpleDocTemplate(
        out,
        pagesize=A4,
        leftMargin=20*mm, rightMargin=20*mm,
        topMargin=18*mm, bottomMargin=22*mm,
        title="rua / mcp-shield 프로젝트 가이드",
        author="Claude Code",
    )

    story = []

    # ── 표지 ─────────────────────────────────────────────────────────────────
    story.append(Spacer(1, 80))
    story.append(Paragraph("rua / mcp-shield", S["cover_title"]))
    story.append(Spacer(1, 12))
    story.append(Paragraph("Go 입문자를 위한 완전 해설서", S["cover_sub"]))
    story.append(Spacer(1, 30))

    badges = [
        [Paragraph("🔐  Ed25519 서명 인증", S["cover_badge"]),
         Paragraph("📝  SQLite 로깅", S["cover_badge"]),
         Paragraph("📊  Prometheus 모니터링", S["cover_badge"])],
    ]
    bt = Table(badges, colWidths=[(W-40*mm)/3]*3)
    bt.setStyle(TableStyle([
        ("BACKGROUND", (0,0), (-1,-1), colors.HexColor("#1565C0")),
        ("TOPPADDING", (0,0), (-1,-1), 8),
        ("BOTTOMPADDING",(0,0),(-1,-1),8),
        ("GRID", (0,0),(-1,-1), 0.5, colors.HexColor("#1976D2")),
    ]))
    story.append(bt)
    story.append(Spacer(1, 20))

    badges2 = [
        [Paragraph("🚀  Go 언어로 작성", S["cover_badge"]),
         Paragraph("🔄  JSON-RPC 프록시", S["cover_badge"]),
         Paragraph("🛡️  MCP 보안 미들웨어", S["cover_badge"])],
    ]
    bt2 = Table(badges2, colWidths=[(W-40*mm)/3]*3)
    bt2.setStyle(TableStyle([
        ("BACKGROUND", (0,0), (-1,-1), colors.HexColor("#0D47A1")),
        ("TOPPADDING", (0,0), (-1,-1), 8),
        ("BOTTOMPADDING",(0,0),(-1,-1),8),
        ("GRID", (0,0),(-1,-1), 0.5, colors.HexColor("#1565C0")),
    ]))
    story.append(bt2)
    story.append(Spacer(1, 40))
    story.append(Paragraph("2026.03.22", S["cover_sub"]))

    story.append(PageBreak())

    # ── 목차 ─────────────────────────────────────────────────────────────────
    story.append(SectionHeader("📋  목차", C_BLUE))
    story.append(Spacer(1, 10))

    toc_items = [
        ("1", "이 프로젝트가 뭐 하는 건가요?",    "전체 개요와 한 줄 설명"),
        ("2", "전체 아키텍처 한눈에 보기",          "큰 그림 다이어그램"),
        ("3", "폴더 구조 완전 해설",               "파일 하나하나 설명"),
        ("4", "Go 핵심 개념 4가지",                "이 코드에서 쓰인 패턴"),
        ("5", "요청 처리 흐름",                    "데이터가 이동하는 길"),
        ("6", "미들웨어 체인",                     "Auth → Log 구조"),
        ("7", "인증(Auth) 상세",                   "Ed25519 서명 검증 원리"),
        ("8", "SQLite 저장소",                     "로그 스키마와 조회"),
        ("9", "설정 시스템",                       "YAML, 환경변수, CLI"),
        ("10", "모니터링",                          "/healthz, /metrics"),
        ("11", "텔레메트리",                        "익명 통계 & 프라이버시"),
        ("12", "빌드 & 실행 & 테스트",             "개발자 치트시트"),
    ]

    toc_data = [[Paragraph(f"  {no}", S["body"]),
                 Paragraph(title, S["body"]),
                 Paragraph(desc, S["small"])]
                for no, title, desc in toc_items]

    toc_ts = TableStyle([
        ("ROWBACKGROUNDS", (0,0), (-1,-1), [C_WHITE, C_GRAY_LT]),
        ("GRID", (0,0), (-1,-1), 0.3, colors.HexColor("#CFD8DC")),
        ("TOPPADDING", (0,0), (-1,-1), 5),
        ("BOTTOMPADDING",(0,0),(-1,-1),5),
        ("LEFTPADDING", (0,0), (-1,-1), 6),
        ("FONTNAME", (0,0), (0,-1), FONT_BOLD),
        ("TEXTCOLOR", (0,0), (0,-1), C_BLUE),
    ])
    toc_tbl = Table(toc_data,
                    colWidths=[20, (W-40*mm)*0.45, (W-40*mm)*0.5 - 20],
                    style=toc_ts)
    story.append(toc_tbl)
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 1. 개요
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("1.  이 프로젝트가 뭐 하는 건가요?", C_BLUE))
    story.append(Spacer(1, 8))

    story.append(Paragraph(
        "rua는 <b>mcp-shield</b>라는 실행 파일을 만드는 Go 프로젝트입니다. "
        "MCP(Model Context Protocol) 서버와 AI 에이전트 사이에 <b>투명하게 끼어드는 보안 미들웨어 프록시</b>입니다.",
        S["body"]
    ))
    story.append(Spacer(1, 6))

    story.append(info_box(
        "쉽게 말하면: AI 에이전트가 MCP 서버와 대화할 때, 중간에서 몰래 감청하며 "
        "누가 보냈는지 확인하고 기록을 남기는 프로그램입니다. MCP 서버는 이 사실을 모릅니다.",
        C_BLUE_LT, C_BLUE, "💡"
    ))
    story.append(Spacer(1, 10))

    story.append(SubHeader("실제 실행 방법"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "# 기존 방식 (MCP 서버 직접 실행)\n"
        "python my_mcp_server.py\n\n"
        "# mcp-shield 사용 (앞에 붙이기만 하면 끝!)\n"
        "mcp-shield python my_mcp_server.py\n"
        "mcp-shield node server.js --port 8080"
    ))
    story.append(Spacer(1, 10))

    story.append(SubHeader("mcp-shield가 하는 일 (4가지)"))
    story.append(Spacer(1, 4))

    jobs = [
        ["🔐", "서명 검증 (Auth)",      "AI 에이전트가 Ed25519로 서명한 요청인지 확인합니다"],
        ["📝", "로그 저장 (Log)",        "모든 요청/응답을 SQLite 데이터베이스에 기록합니다"],
        ["📊", "모니터링 (Monitor)",     "Prometheus 메트릭과 /healthz 헬스체크를 제공합니다"],
        ["📡", "텔레메트리 (Telemetry)", "익명화된 사용 통계를 서버로 전송합니다 (기본 꺼짐)"],
    ]
    jobs_tbl = make_table(["아이콘", "기능", "설명"], jobs,
                          [30, 120, W-40*mm-170], C_BLUE)
    story.append(jobs_tbl)
    story.append(Spacer(1, 8))

    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 2. 전체 아키텍처
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("2.  전체 아키텍처 한눈에 보기", C_BLUE))
    story.append(Spacer(1, 10))
    story.append(ArchDiagram())
    story.append(Spacer(1, 10))

    story.append(info_box(
        "stdin / stdout란?  프로세스끼리 데이터를 주고받는 표준 통로입니다. "
        "키보드 입력이 stdin, 화면 출력이 stdout — mcp-shield는 이 두 통로 사이에 자리잡습니다.",
        C_ORANGE_LT, C_ORANGE, "📌"
    ))
    story.append(Spacer(1, 10))

    story.append(SubHeader("왜 이런 구조인가요?"))
    story.append(Spacer(1, 4))
    story.append(Paragraph(
        "MCP 서버는 <b>수정할 필요가 없습니다</b>. mcp-shield가 앞에 붙기만 하면 "
        "기존 서버 코드를 그대로 두고 보안/로깅 기능이 추가됩니다. "
        "이를 <b>투명 프록시 패턴(Transparent Proxy)</b>이라고 합니다.",
        S["body"]
    ))
    story.append(Spacer(1, 8))

    # 방향별 처리 표
    dir_data = [
        ["→ 요청 방향\n(stdin: AI→MCP)", "PipelineIn",  "요청 파싱 → Auth → Log → MCP 서버로 전달"],
        ["← 응답 방향\n(stdout: MCP→AI)", "PipelineOut", "응답 파싱 → Log → AI 에이전트로 전달"],
    ]
    story.append(make_table(
        ["방향", "처리 함수", "미들웨어 체인"],
        dir_data,
        [90, 80, W-40*mm-190],
        C_BLUE
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 3. 폴더 구조
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("3.  폴더 구조 완전 해설", C_BLUE))
    story.append(Spacer(1, 8))
    story.append(FolderTree())
    story.append(Spacer(1, 8))

    story.append(info_box(
        "internal/ 이 특별한 이유: Go에서 internal 폴더 안의 패키지는 "
        "이 모듈 내부에서만 쓸 수 있습니다. 외부 프로젝트가 가져다 쓸 수 없다는 "
        "명시적인 표시입니다.",
        C_PURPLE_LT, C_PURPLE, "🔒"
    ))
    story.append(Spacer(1, 6))

    pkg_data = [
        ["auth/",       "Ed25519 공개키로 서명 검증. did:key: DID 형식도 지원"],
        ["config/",     "YAML 파일 로딩. 환경변수·CLI 플래그 오버라이드 처리"],
        ["jsonrpc/",    "줄 단위 JSON-RPC 메시지 파싱. 요청/응답/알림 구분"],
        ["logging/",    "slog 기반 구조화 로거. JSON/text 포맷 선택 가능"],
        ["middleware/", "Middleware 인터페이스 정의. Chain으로 순서대로 실행"],
        ["monitor/",    "HTTP 서버 (127.0.0.1:9090). /healthz, /metrics 엔드포인트"],
        ["process/",    "os/exec로 자식 프로세스 실행. stdin/stdout 파이프 연결"],
        ["storage/",    "SQLite DB 열기/마이그레이션/삽입/조회/자동 purge"],
        ["telemetry/",  "링 버퍼 + 배치 전송. 차분 프라이버시(DP) 노이즈 적용"],
    ]
    story.append(make_table(
        ["패키지", "역할 요약"],
        pkg_data,
        [90, W-40*mm-110],
        C_BLUE
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 4. Go 핵심 개념
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("4.  Go 핵심 개념 4가지", C_GREEN))
    story.append(Spacer(1, 8))

    story.append(SubHeader("① goroutine — 가벼운 동시 실행 단위"))
    story.append(Spacer(1, 4))
    story.append(Paragraph(
        "go 키워드 하나로 새 goroutine이 시작됩니다. Java 스레드보다 훨씬 가볍고 (초기 메모리 ~2KB), "
        "수만 개를 동시에 띄워도 됩니다. mcp-shield는 아래 goroutine들을 동시에 실행합니다:",
        S["body"]
    ))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "go telCol.Run(telCtx)   // 텔레메트리: 60초마다 배치 전송\n"
        "go lm.writer()          // 로그: 채널에서 꺼내 SQLite 저장\n"
        "go PipelineIn(...)      // 파이프라인: stdin 읽어서 처리\n"
        "go PipelineOut(...)     // 파이프라인: stdout 읽어서 처리"
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("② channel — goroutine 간 안전한 데이터 통로"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "// 버퍼 512짜리 채널 생성\n"
        "writeCh := make(chan ActionLog, 512)\n\n"
        "// 보내기 (채널이 꽉 차면 버림 — non-blocking)\n"
        "select {\n"
        "case writeCh <- log:      // 성공\n"
        "default:                  // 채널 꽉 참 → 드롭\n"
        "    logger.Warn(\"channel full\")\n"
        "}\n\n"
        "// 받기 (채널이 닫힐 때까지 반복)\n"
        "for log := range writeCh {\n"
        "    db.Insert(log)\n"
        "}"
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("③ interface — 묵시적 구현 계약"))
    story.append(Spacer(1, 4))
    story.append(Paragraph(
        "Go의 interface는 <b>명시적 선언 없이</b> 메서드만 맞으면 자동으로 구현됩니다. "
        "Java의 implements 키워드가 필요 없습니다.",
        S["body"]
    ))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "// 인터페이스 정의\n"
        "type Middleware interface {\n"
        "    ProcessRequest(ctx, *Request) (*Request, error)\n"
        "    ProcessResponse(ctx, *Response) (*Response, error)\n"
        "}\n\n"
        "// AuthMiddleware가 두 메서드를 구현하면 자동으로 Middleware\n"
        "// implements Middleware 같은 선언 필요 없음!"
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("④ context — 취소 신호 전파"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "ctx, cancel := context.WithCancel(context.Background())\n"
        "defer cancel()  // 함수 종료 시 자동 취소\n\n"
        "go telCol.Run(ctx)  // ctx가 취소되면 Run()도 정지\n\n"
        "// Run() 내부에서 이렇게 감지:\n"
        "select {\n"
        "case <-ticker.C:   // 60초마다\n"
        "    c.flush()\n"
        "case <-ctx.Done(): // 취소 신호 수신 → 종료\n"
        "    c.flush(); return\n"
        "}"
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 5. 요청 처리 흐름
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("5.  요청 처리 흐름", C_BLUE))
    story.append(Spacer(1, 8))
    story.append(PipelineFlow())
    story.append(Spacer(1, 10))

    story.append(SubHeader("단계별 상세 설명"))
    story.append(Spacer(1, 4))

    flow_steps = [
        ["1", "stdin 수신",       "AI 에이전트가 보낸 JSON-RPC 요청이 mcp-shield의 stdin으로 들어옴"],
        ["2", "JSON 파싱",        "jsonrpc.Parser가 줄 단위로 읽어 Request/Response/Notification 구분"],
        ["3", "Auth 미들웨어",    "Ed25519 서명 확인. 실패 시 closed 모드면 에러 반환, open 모드면 경고만"],
        ["4", "Log 미들웨어",     "요청을 SQLite에 저장 (비동기). 응답 도착 시 레이턴시 계산해서 업데이트"],
        ["5", "MCP 서버 전달",    "검증된 요청을 자식 프로세스(python/node)의 stdin으로 전달"],
        ["↩", "응답 역방향",      "MCP 서버 stdout → PipelineOut → Log MW → AI 에이전트 stdout"],
    ]
    story.append(make_table(
        ["단계", "이름", "설명"],
        flow_steps,
        [25, 90, W-40*mm-135],
        C_BLUE
    ))
    story.append(Spacer(1, 8))

    story.append(info_box(
        "요청이 Auth에서 거부되면?  MCP 서버에는 전달하지 않고, "
        "mcp-shield가 직접 JSON-RPC 에러 응답을 AI 에이전트에게 씁니다. "
        "MCP 서버는 거부된 요청을 전혀 모릅니다.",
        C_RED_LT, C_RED, "⚠️"
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 6. 미들웨어 체인
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("6.  미들웨어 체인", C_BLUE))
    story.append(Spacer(1, 8))
    story.append(MiddlewareChain())
    story.append(Spacer(1, 10))

    story.append(SubHeader("Chain 동작 원리"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "// 미들웨어 순서대로 등록\n"
        "chain := middleware.NewChain(authMW, logMW)\n\n"
        "// 요청 처리 (auth → log 순서)\n"
        "func (c *Chain) ProcessRequest(ctx, req) (*Request, []byte, error) {\n"
        "    cur := req\n"
        "    for _, m := range c.items {    // authMW 먼저, 그 다음 logMW\n"
        "        next, err := m.ProcessRequest(ctx, cur)\n"
        "        if err != nil {             // 에러 발생 시 즉시 중단\n"
        "            return nil, errorPayload, err\n"
        "        }\n"
        "        cur = next\n"
        "    }\n"
        "    return cur, nil, nil\n"
        "}"
    ))
    story.append(Spacer(1, 6))

    story.append(SubHeader("PassthroughMiddleware — 임베딩 패턴"))
    story.append(Spacer(1, 4))
    story.append(Paragraph(
        "새 미들웨어를 만들 때 <b>모든 메서드를 구현하지 않아도 됩니다</b>. "
        "PassthroughMiddleware를 임베딩하면 기본 no-op 구현이 자동으로 생깁니다.",
        S["body"]
    ))
    story.append(code_block(
        "type LogMiddleware struct {\n"
        "    middleware.PassthroughMiddleware  // ← 이게 임베딩\n"
        "    db      *DB\n"
        "    writeCh chan ActionLog\n"
        "}\n\n"
        "// ProcessResponse만 구현. ProcessRequest는 Passthrough가 처리"
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 7. 인증 상세
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("7.  인증(Auth) 상세", C_ORANGE))
    story.append(Spacer(1, 8))

    story.append(SubHeader("Ed25519 서명이란?"))
    story.append(Spacer(1, 4))
    story.append(Paragraph(
        "Ed25519는 공개키 암호화 알고리즘입니다. AI 에이전트는 <b>개인키</b>로 요청에 서명하고, "
        "mcp-shield는 <b>공개키</b>로 서명을 검증합니다. 개인키 없이는 위조가 불가능합니다.",
        S["body"]
    ))
    story.append(Spacer(1, 8))

    story.append(AuthFlow())
    story.append(Spacer(1, 8))

    story.append(SubHeader("JSON-RPC 요청에 서명이 어떻게 포함되나요?"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        '{\n'
        '  "jsonrpc": "2.0",\n'
        '  "method": "tools/call",\n'
        '  "id": 1,\n'
        '  "params": {\n'
        '    "name": "my_tool",\n'
        '    "_mcp_agent_id": "did:key:z6Mk...",   ← 에이전트 ID\n'
        '    "_mcp_signature": "a3f8b2..."          ← Ed25519 서명 (hex)\n'
        '  }\n'
        '}'
    ))
    story.append(Spacer(1, 6))

    auth_modes = [
        ["open  (기본)", "서명 실패해도 경고 로그만 찍고 요청 통과",       "관찰/모니터링 용도"],
        ["closed",       "서명 실패 시 즉시 JSON-RPC 에러 반환 & 차단",    "프로덕션 보안 강화"],
    ]
    story.append(make_table(
        ["모드", "동작", "용도"],
        auth_modes,
        [60, 200, W-40*mm-280],
        C_ORANGE
    ))
    story.append(Spacer(1, 6))

    auth_status = [
        ["verified",  "서명 검증 성공",              "Prometheus 카운터 +1, INFO 로그"],
        ["failed",    "서명 검증 실패 또는 키 없음", "Prometheus 카운터 +1, WARN 로그"],
        ["unsigned",  "서명 필드 자체가 없음",        "Prometheus 카운터 +1, WARN 로그"],
    ]
    story.append(make_table(
        ["인증 상태", "의미", "처리"],
        auth_status,
        [70, 130, W-40*mm-220],
        C_ORANGE
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 8. SQLite 저장소
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("8.  SQLite 저장소", C_GREEN))
    story.append(Spacer(1, 8))

    story.append(SubHeader("action_logs 테이블 스키마"))
    story.append(Spacer(1, 4))

    schema_rows = [
        ["id",            "INTEGER",  "자동 증가 기본키"],
        ["timestamp",     "DATETIME", "기록 시각 (UTC)"],
        ["agent_id_hash", "TEXT",     "sha256(agentID) — 원본 ID는 저장 안 함 (프라이버시)"],
        ["method",        "TEXT",     "JSON-RPC 메서드명 (예: tools/call)"],
        ["direction",     "TEXT",     "in (요청) / out (응답)"],
        ["success",       "BOOLEAN",  "성공 여부"],
        ["latency_ms",    "REAL",     "응답 레이턴시 (ms). 요청 행에는 0"],
        ["payload_size",  "INTEGER",  "params 또는 result 바이트 수"],
        ["auth_status",   "TEXT",     "verified / failed / unsigned"],
        ["error_code",    "TEXT",     "에러 코드 (있을 때만)"],
    ]
    story.append(make_table(
        ["컬럼", "타입", "설명"],
        schema_rows,
        [90, 65, W-40*mm-175],
        C_GREEN
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("비동기 저장 구조"))
    story.append(Spacer(1, 4))
    story.append(Paragraph(
        "SQLite 쓰기는 요청 처리를 <b>블로킹하지 않습니다</b>. 채널(channel)을 통해 "
        "별도 goroutine이 처리합니다:",
        S["body"]
    ))
    story.append(Spacer(1, 4))

    async_steps = [
        ["①", "ProcessRequest 호출",   "pending map에 요청 저장 (reqID → 시작시각, method)"],
        ["②", "ProcessResponse 호출",  "pending에서 꺼내 레이턴시 계산 후 ActionLog 생성"],
        ["③", "채널로 전송",            "select { case writeCh <- log: default: drop }  ← non-blocking"],
        ["④", "writer() goroutine",    "for log := range writeCh { db.Insert(log) }  ← 실제 저장"],
    ]
    story.append(make_table(
        ["단계", "위치", "동작"],
        async_steps,
        [25, 110, W-40*mm-155],
        C_GREEN
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("로그 조회 CLI"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "# 최근 50개 (기본)\n"
        "mcp-shield logs\n\n"
        "# 최근 100개, JSON 포맷\n"
        "mcp-shield logs --last 100 --format json\n\n"
        "# 특정 메서드, 1시간 이내\n"
        "mcp-shield logs --method tools/call --since 1h\n\n"
        "# 특정 에이전트 필터 (내부적으로 sha256 변환됨)\n"
        "mcp-shield logs --agent did:key:z6Mk..."
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 9. 설정 시스템
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("9.  설정 시스템", C_BLUE))
    story.append(Spacer(1, 8))

    story.append(SubHeader("설정 우선순위 (높을수록 먼저 적용)"))
    story.append(Spacer(1, 4))
    story.append(ConfigPriority())
    story.append(Spacer(1, 10))

    story.append(SubHeader("주요 설정 항목"))
    story.append(Spacer(1, 4))

    cfg_rows = [
        ["server.monitor_addr",      "127.0.0.1:9090", "MCP_SHIELD_MONITOR_ADDR",      "모니터링 HTTP 서버 주소"],
        ["security.mode",            "open",           "MCP_SHIELD_SECURITY_MODE",      "open / closed"],
        ["security.key_store_path",  "keys.yaml",      "MCP_SHIELD_KEY_STORE_PATH",     "공개키 파일 경로"],
        ["logging.level",            "info",           "MCP_SHIELD_LOG_LEVEL",          "debug/info/warn/error"],
        ["logging.format",           "json",           "MCP_SHIELD_LOG_FORMAT",         "json / text"],
        ["storage.db_path",          "mcp-shield.db",  "MCP_SHIELD_DB_PATH",            "SQLite 파일 경로"],
        ["storage.retention_days",   "30",             "MCP_SHIELD_RETENTION_DAYS",     "로그 보존 기간(일)"],
        ["telemetry.enabled",        "false",          "MCP_SHIELD_TELEMETRY_ENABLED",  "익명 통계 활성화"],
    ]
    story.append(make_table(
        ["설정 키", "기본값", "환경변수", "설명"],
        cfg_rows,
        [120, 70, 140, W-40*mm-350],
        C_BLUE
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("YAML 설정 예시 (mcp-shield.yaml)"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "server:\n"
        "  monitor_addr: \"127.0.0.1:9090\"\n\n"
        "security:\n"
        "  mode: \"open\"          # closed로 바꾸면 미인증 요청 차단\n"
        "  key_store_path: \"keys.yaml\"\n\n"
        "logging:\n"
        "  level: \"info\"         # debug로 바꾸면 상세 로그\n"
        "  format: \"json\"\n\n"
        "storage:\n"
        "  db_path: \"mcp-shield.db\"\n"
        "  retention_days: 30    # 30일 지난 로그 자동 삭제\n\n"
        "telemetry:\n"
        "  enabled: false        # true로 바꾸면 익명 통계 전송"
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 10. 모니터링
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("10.  모니터링", C_GREEN))
    story.append(Spacer(1, 8))

    story.append(SubHeader("HTTP 엔드포인트 (기본: 127.0.0.1:9090)"))
    story.append(Spacer(1, 4))

    ep_rows = [
        ["/healthz", "GET", "JSON 헬스체크. 자식 프로세스 생존 확인 (kill -0).\nstatus: healthy / degraded"],
        ["/metrics", "GET", "Prometheus 형식 메트릭 텍스트 반환"],
    ]
    story.append(make_table(
        ["엔드포인트", "메서드", "설명"],
        ep_rows,
        [80, 55, W-40*mm-155],
        C_GREEN
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("Prometheus 메트릭"))
    story.append(Spacer(1, 4))

    prom_rows = [
        ["mcp_shield_messages_total",          "Counter",   "direction, method",  "처리된 메시지 총 개수"],
        ["mcp_shield_auth_total",              "Counter",   "status",             "인증 결과 카운터"],
        ["mcp_shield_message_latency_seconds", "Histogram", "method",             "메서드별 응답 레이턴시"],
        ["mcp_shield_child_process_up",        "Gauge",     "-",                  "자식 프로세스 생존 여부 (1/0)"],
    ]
    story.append(make_table(
        ["메트릭명", "타입", "레이블", "설명"],
        prom_rows,
        [160, 60, 70, W-40*mm-310],
        C_GREEN
    ))
    story.append(Spacer(1, 8))

    story.append(code_block(
        "# 헬스체크\n"
        "curl http://localhost:9090/healthz\n"
        "# 응답: {\"status\":\"healthy\",\"child_pid\":12345}\n\n"
        "# 메트릭 확인\n"
        "curl http://localhost:9090/metrics\n"
        "# 응답: mcp_shield_messages_total{direction=\"in\",method=\"tools/call\"} 42"
    ))
    story.append(Spacer(1, 8))

    # ════════════════════════════════════════════════════════════════════
    # 11. 텔레메트리
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("11.  텔레메트리 (익명 통계)", C_PURPLE))
    story.append(Spacer(1, 8))

    story.append(info_box(
        "텔레메트리는 기본적으로 꺼져 있습니다 (enabled: false). "
        "활성화해도 메시지 내용(content)은 절대 수집하지 않습니다. "
        "메타데이터(method, success, 시각)만 수집하며 여러 프라이버시 기법이 적용됩니다.",
        C_PURPLE_LT, C_PURPLE, "🔏"
    ))
    story.append(Spacer(1, 8))

    priv_rows = [
        ["Agent ID 익명화",     "sha256(salt + agentID) 해시로 변환. 원본 ID는 전송 안 함"],
        ["IP K-익명성",         "IPv4는 /24 마스킹 (끝 8비트 제거), IPv6는 /48 마스킹"],
        ["차분 프라이버시 (DP)", "success 필드를 1/(1+e^ε) 확률로 무작위 flip (기본 ε=1.0)"],
        ["링 버퍼",             "최대 10,000개 이벤트만 메모리에 보관. 초과 시 오래된 것 버림"],
        ["배치 전송",           "60초마다 gzip 압축해서 POST /telemetry/ingest"],
    ]
    story.append(make_table(
        ["프라이버시 기법", "설명"],
        priv_rows,
        [120, W-40*mm-140],
        C_PURPLE
    ))
    story.append(PageBreak())

    # ════════════════════════════════════════════════════════════════════
    # 12. 빌드 & 실행 & 테스트
    # ════════════════════════════════════════════════════════════════════
    story.append(SectionHeader("12.  빌드 & 실행 & 테스트 치트시트", C_ORANGE))
    story.append(Spacer(1, 8))

    story.append(SubHeader("빌드 & 실행"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "cd rua\n\n"
        "# 빌드 (바이너리 생성)\n"
        "go build ./cmd/mcp-shield/\n\n"
        "# 빌드 없이 바로 실행\n"
        "go run ./cmd/mcp-shield/ python my_server.py\n\n"
        "# 상세 로그로 실행\n"
        "./mcp-shield --verbose python server.py\n\n"
        "# closed 모드 (미인증 요청 차단)\n"
        "MCP_SHIELD_SECURITY_MODE=closed ./mcp-shield python server.py\n\n"
        "# 모니터링 주소 변경\n"
        "./mcp-shield --monitor-addr 0.0.0.0:9090 python server.py"
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("테스트"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "# 전체 테스트 실행\n"
        "go test ./...\n\n"
        "# 특정 패키지만\n"
        "go test ./internal/auth/...\n"
        "go test ./internal/storage/...\n\n"
        "# 특정 테스트 함수만\n"
        "go test -run TestLogMiddleware ./internal/storage/...\n\n"
        "# 상세 출력 (fmt.Println 등 보임)\n"
        "go test -v ./internal/telemetry/...\n\n"
        "# 커버리지 측정\n"
        "go test -cover ./..."
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("로그 조회"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "# 최근 50개 (기본, 테이블 형식)\n"
        "mcp-shield logs\n\n"
        "# 최근 100개, JSON 형식\n"
        "mcp-shield logs --last 100 --format json\n\n"
        "# 특정 메서드, 최근 1시간\n"
        "mcp-shield logs --method tools/call --since 1h\n\n"
        "# 특정 에이전트\n"
        "mcp-shield logs --agent did:key:z6Mk..."
    ))
    story.append(Spacer(1, 8))

    story.append(SubHeader("의존성 관리"))
    story.append(Spacer(1, 4))
    story.append(code_block(
        "# go.mod = package.json (의존성 목록)\n"
        "# go.sum = package-lock.json (정확한 버전 고정)\n\n"
        "# 새 패키지 추가\n"
        "go get github.com/some/package\n\n"
        "# 불필요한 의존성 정리\n"
        "go mod tidy\n\n"
        "# 주요 의존 패키지\n"
        "# - github.com/spf13/cobra   : CLI 프레임워크\n"
        "# - gopkg.in/yaml.v3         : YAML 파싱\n"
        "# - modernc.org/sqlite        : SQLite (CGO 없이 순수 Go)\n"
        "# - github.com/prometheus/...  : 메트릭"
    ))

    story.append(Spacer(1, 10))
    story.append(HRFlowable(width=W-40*mm, color=colors.HexColor("#B0BEC5")))
    story.append(Spacer(1, 6))
    story.append(Paragraph(
        "이 문서는 rua / mcp-shield 프로젝트 코드베이스를 기반으로 자동 생성되었습니다.",
        S["small"]
    ))

    # ── 빌드 ─────────────────────────────────────────────────────────────────
    doc.build(story,
              onFirstPage=cover_page,
              onLaterPages=on_page)
    print(f"✅  PDF 생성 완료: {out}")


if __name__ == "__main__":
    build()
