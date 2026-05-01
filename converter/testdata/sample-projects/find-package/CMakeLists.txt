cmake_minimum_required(VERSION 3.20)
project(usepkg LANGUAGES C VERSION 0.1.0)

# ZLIB is a system-installed library cmake's FindZLIB.cmake locates
# via /usr/lib. Using a real linked dep (vs. INTERFACE-only like
# Threads) ensures the codemodel records a link fragment the
# converter's lower pass can match against the imports manifest.
find_package(ZLIB REQUIRED)

add_library(usepkg SHARED src/usepkg.c)
target_include_directories(usepkg PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(usepkg PUBLIC ZLIB::ZLIB)
set_target_properties(usepkg PROPERTIES VERSION 0.1.0 SOVERSION 0)

install(TARGETS usepkg
        EXPORT usepkgTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT usepkgTargets
        FILE usepkgTargets.cmake
        NAMESPACE usepkg::
        DESTINATION lib/cmake/usepkg)
