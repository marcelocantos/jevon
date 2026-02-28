#include "App.h"

#include <ge/GpuContext.h>
#include <spdlog/spdlog.h>
#include <cmath>

App::App(ge::GpuContext& ctx) {
    SPDLOG_INFO("App created ({}x{})", ctx.width(), ctx.height());
}

App::~App() = default;

void App::update(float dt) {
    time_ += dt;
}

void App::render(ge::GpuContext& ctx, wgpu::TextureView target, int w, int h) {
    float r = 0.5f * (1.0f + std::sin(time_ * 0.5f));
    float g = 0.5f * (1.0f + std::sin(time_ * 0.7f + 2.0f));
    float b = 0.5f * (1.0f + std::sin(time_ * 1.1f + 4.0f));

    wgpu::RenderPassColorAttachment colorAttachment{};
    colorAttachment.view = target;
    colorAttachment.loadOp = wgpu::LoadOp::Clear;
    colorAttachment.storeOp = wgpu::StoreOp::Store;
    colorAttachment.clearValue = {r, g, b, 1.0f};

    wgpu::RenderPassDescriptor passDesc{};
    passDesc.colorAttachmentCount = 1;
    passDesc.colorAttachments = &colorAttachment;

    auto encoder = ctx.device().CreateCommandEncoder();
    auto pass = encoder.BeginRenderPass(&passDesc);
    pass.End();

    auto commands = encoder.Finish();
    ctx.queue().Submit(1, &commands);
}

void App::event(const SDL_Event& e) {
}
