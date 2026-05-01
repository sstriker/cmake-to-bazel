#pragma once

namespace iflib {

template <typename T>
constexpr T add_one(T v) noexcept {
    return v + T{1};
}

} // namespace iflib
