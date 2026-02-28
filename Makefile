BUILD_DIR := build
CXX       := clang++

-include ge/Module.mk
ge/Module.mk:
	git submodule update --init --recursive

# ── Flags ────────────────────────────────────────────
CXXFLAGS   := -std=c++20 -O2 -Wall $(ge/INCLUDES)
SDL_CFLAGS :=
SDL_LIBS   := $(ge/SDL_LIBS)
FRAMEWORKS := -framework Metal -framework QuartzCore -framework Foundation \
              -framework CoreFoundation -framework IOKit -framework IOSurface \
              -framework CoreGraphics -framework CoreServices \
              -framework AudioToolbox -framework AVFoundation -framework CoreMedia \
              -framework CoreVideo -framework GameController -framework CoreHaptics \
              -framework CoreMotion -framework ImageIO

# ── C++ app ──────────────────────────────────────────
SRC := src/main.cpp src/App.cpp
OBJ := $(patsubst %.cpp,$(BUILD_DIR)/%.o,$(SRC))
APP := bin/dais

COMPILE_DB_DEPS += $(SRC) Makefile

# ── Default target ───────────────────────────────────
.PHONY: all
all: $(APP) daisd remote

# ── C++ binary ───────────────────────────────────────
$(APP): $(OBJ) $(ge/SESSION_WIRE_OBJ) $(ge/LIB) $(ge/FRAMEWORK_LIBS)
	@mkdir -p $(@D)
	$(CXX) $(OBJ) $(ge/SESSION_WIRE_OBJ) $(ge/LIB) $(ge/DAWN_LIBS) \
		$(FRAMEWORKS) $(SDL_LIBS) -o $@

$(BUILD_DIR)/src/%.o: src/%.cpp
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) $(SDL_CFLAGS) -MMD -MP -c $< -o $@

-include $(OBJ:.o=.d)

# ── Player ───────────────────────────────────────────
.PHONY: player
player: $(ge/PLAYER)

# ── Go coordinator ───────────────────────────────────
.PHONY: daisd
daisd: bin/daisd

bin/daisd: $(shell find cmd internal -name '*.go' 2>/dev/null)
	@mkdir -p bin
	go build -o bin/daisd ./cmd/daisd

# ── Terminal remote ──────────────────────────────────
.PHONY: remote
remote: bin/remote

bin/remote: $(shell find cmd/remote -name '*.go' 2>/dev/null)
	@mkdir -p bin
	go build -o bin/remote ./cmd/remote

# ── Run ──────────────────────────────────────────────
.PHONY: run run-app run-daisd run-remote
run-app: $(APP)
	$(APP)

run-daisd: bin/daisd
	bin/daisd

run-remote: bin/remote
	bin/remote

run: $(APP) bin/daisd
	@trap 'kill 0' INT TERM; \
	bin/daisd & \
	$(APP) & \
	wait

# ── Setup ────────────────────────────────────────────
.PHONY: init
init: ge/init
	@echo "── dais project setup ──"
	@command -v go >/dev/null 2>&1 || { echo "ERROR: Go not found. Install from https://go.dev/dl/"; exit 1; }
	@echo "  Go: $$(go version)"
	@go mod download
	@echo "  Go dependencies downloaded"
	$(ge/INIT_DONE)

# ── Test ─────────────────────────────────────────────
.PHONY: test test-go
test-go:
	go test ./...

test: test-go
