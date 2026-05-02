cmake_minimum_required(VERSION 3.20)
project(pcuser LANGUAGES C VERSION 0.1.0)

# pkg_check_modules is the cmake idiom for consuming non-cmake-
# aware deps that ship a .pc file. find_package(PkgConfig)
# loads the wrapper module; pkg_check_modules calls into
# pkg-config and either:
#   - exposes flags via variables (GLIB_CFLAGS / GLIB_LDFLAGS),
#     or
#   - synthesizes a PkgConfig::<NAME> IMPORTED INTERFACE
#     target via the IMPORTED_TARGET keyword.
#
# IMPORTED_TARGET is the modern shape — same usage pattern
# as find_package's IMPORTED targets. The resulting
# `PkgConfig::ZLIB` target carries the cflags/ldflags as
# INTERFACE_* properties; consuming via
# target_link_libraries flows them through cmake's
# transitive propagation.
#
# For the converter: the dep edge surfaces in t.Link.
# CommandFragments (SHARED) or trace's
# target_link_libraries (STATIC). The imports manifest maps
# `PkgConfig::ZLIB` to the same Bazel label as `ZLIB::ZLIB`
# would — they're alternative cmake names for the same
# underlying system library.
find_package(PkgConfig REQUIRED)
pkg_check_modules(ZLIB REQUIRED IMPORTED_TARGET zlib)

add_library(pcuser SHARED src/pcuser.c)
target_include_directories(pcuser PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(pcuser PUBLIC PkgConfig::ZLIB)

install(TARGETS pcuser
        EXPORT pcuserTargets
        LIBRARY DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT pcuserTargets
        FILE pcuserTargets.cmake
        NAMESPACE pcuser::
        DESTINATION lib/cmake/pcuser)
