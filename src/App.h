#pragma once

#include <webgpu/webgpu_cpp.h>
#include <SDL3/SDL_events.h>

namespace ge { class GpuContext; }

class App {
public:
    explicit App(ge::GpuContext& ctx);
    ~App();

    void update(float dt);
    void render(ge::GpuContext& ctx, wgpu::TextureView target, int w, int h);
    void event(const SDL_Event& e);

private:
    float time_ = 0.0f;
};
