cmake_minimum_required(VERSION 3.20)
project(cons LANGUAGES C VERSION 0.1.0)

# Consumer cmake element: depends on `prod` (a kind:cmake
# element). At convert-element time, write-a's cmake handler
# stages prod's synthesized cmake-config bundle under
# $PREFIX/lib/cmake/prod/ and passes --prefix-dir=$PREFIX.
# find_package(prod CONFIG) below resolves against that
# bundle, surfacing `prod::prod` as an IMPORTED static
# library; target_link_libraries flows it into the
# codemodel's link fragment / trace.
find_package(prod CONFIG REQUIRED)

add_library(cons STATIC src/cons.c)
target_include_directories(cons PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(cons PUBLIC prod::prod)

install(TARGETS cons
        EXPORT consTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT consTargets
        FILE consTargets.cmake
        NAMESPACE cons::
        DESTINATION lib/cmake/cons)
