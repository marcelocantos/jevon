#include "App.h"

#include <ge/Session.h>
#include <spdlog/spdlog.h>

int main() {
    SPDLOG_INFO("dais starting");

    for (bool done = false; !done;) {
        ge::Session session;
        auto& ctx = session.gpu();
        App app(ctx);

        done = !session.run({
            .onUpdate = [&](float dt) { app.update(dt); },
            .onRender = [&](wgpu::TextureView target, int w, int h) {
                app.render(ctx, target, w, h);
            },
            .onEvent = [&](const SDL_Event& e) { app.event(e); },
        });
    }

    SPDLOG_INFO("dais exiting");
}
