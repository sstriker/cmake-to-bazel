cmake_minimum_required(VERSION 3.20)
project(usepkgst LANGUAGES C VERSION 0.1.0)

# STATIC variant of the find-package fixture. The wrinkle vs.
# the SHARED build: a static archive doesn't run a link step,
# so cmake's codemodel records no Link.CommandFragments and
# no Dependencies for IMPORTED targets like ZLIB::ZLIB.
# Both upstream channels lower normally consults are empty.
# Trace's target_link_libraries call is the only ground
# truth; lower's STATIC fallback consults it to surface the
# IMPORTED dep.
find_package(ZLIB REQUIRED)

add_library(usepkgst STATIC src/usepkgst.c)
target_include_directories(usepkgst PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(usepkgst PUBLIC ZLIB::ZLIB)

install(TARGETS usepkgst
        EXPORT usepkgstTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT usepkgstTargets
        FILE usepkgstTargets.cmake
        NAMESPACE usepkgst::
        DESTINATION lib/cmake/usepkgst)
